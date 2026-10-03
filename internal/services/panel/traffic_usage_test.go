package panel

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func mustShanghai(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestWriteTrafficBucketsUpsertsMinuteAndHourly(t *testing.T) {
	db := testDB(t)
	minute := time.Date(2026, 10, 3, 10, 15, 0, 0, time.UTC)
	write := func(bucket time.Time, up, down, rate uint64, tcp int) {
		t.Helper()
		err := writeTrafficBuckets(db, []trafficBucketDelta{{
			Key:   trafficBucketKey{ServerID: 1, Bucket: bucket},
			Value: trafficBucketValue{Upload: up, Download: down, UploadRate: rate, TCPConnections: tcp},
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	write(minute, 10, 100, 50, 9)
	write(minute, 5, 50, 20, 3) // same minute: bytes add, rate keeps max, conns keep latest
	write(minute.Add(10*time.Minute), 1, 1, 70, 4)

	var rows []model.TrafficRecord
	if err := db.Order("bucket").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Upload != 15 || rows[0].Download != 150 || rows[0].UploadRate != 50 || rows[0].TCPConnections != 3 {
		t.Fatalf("minute rows = %+v", rows)
	}
	var hourly []model.TrafficHourly
	if err := db.Find(&hourly).Error; err != nil {
		t.Fatal(err)
	}
	if len(hourly) != 1 {
		t.Fatalf("hourly rows = %+v", hourly)
	}
	h := hourly[0]
	if !h.Bucket.Equal(minute.Truncate(time.Hour)) || h.Upload != 16 || h.Download != 151 || h.UploadRate != 70 || h.TCPConnections != 9 {
		t.Fatalf("hourly = %+v", h)
	}
}

func TestTrafficWriterBuffersUntilFlushAndDropsDeletedServers(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "n", AgentToken: "writer-token"}
	other := model.Server{Name: "o", AgentToken: "writer-token-2"}
	for _, s := range []*model.Server{&server, &other} {
		if err := db.Create(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	w := newTrafficWriter(db)
	now := time.Now().Unix()
	for _, id := range []uint{server.ID, other.ID} {
		for _, total := range []uint64{100, 300} {
			if err := w.record(id, &protocol.TrafficSnapshot{SampledAt: now, UploadTotal: total, DownloadTotal: total}); err != nil {
				t.Fatal(err)
			}
		}
	}
	var count int64
	db.Model(&model.TrafficRecord{}).Count(&count)
	if count != 0 {
		t.Fatalf("history written before flush: %d rows", count)
	}
	w.dropServer(other.ID)
	if err := w.flush(); err != nil {
		t.Fatal(err)
	}
	var rows []model.TrafficRecord
	db.Find(&rows)
	if len(rows) != 1 || rows[0].ServerID != server.ID || rows[0].Upload != 200 {
		t.Fatalf("rows after flush = %+v", rows)
	}
}

func TestTrafficSampleTimeIgnoresSkewedAgentClock(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 30, 0, time.UTC)
	if got := trafficSampleTime(now.Add(-20*time.Second).Unix(), now); !got.Equal(now.Add(-20 * time.Second)) {
		t.Fatalf("small skew not trusted: %v", got)
	}
	for _, skewed := range []time.Time{now.Add(-10 * time.Minute), now.Add(5 * time.Minute)} {
		if got := trafficSampleTime(skewed.Unix(), now); !got.Equal(now) {
			t.Fatalf("skewed %v -> %v, want panel time", skewed, got)
		}
	}
}

func TestDailyTrafficAlignsToConfiguredTimezone(t *testing.T) {
	db := testDB(t)
	loc := mustShanghai(t)
	// 2026-10-03 02:00 in Shanghai is 2026-10-02 18:00 UTC: it belongs to
	// Oct 3 locally even though the UTC date is Oct 2.
	rows := []model.TrafficHourly{
		{ServerID: 1, Bucket: time.Date(2026, 10, 2, 18, 0, 0, 0, time.UTC), Upload: 7},
		{ServerID: 1, Bucket: time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC), Upload: 3}, // Oct 2 23:00 local
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, loc)
	cfg := trafficRanges["7d"]
	start, end := trafficWindow(now, cfg, loc)
	if !end.Equal(time.Date(2026, 10, 4, 0, 0, 0, 0, loc)) || !start.Equal(time.Date(2026, 9, 27, 0, 0, 0, 0, loc)) {
		t.Fatalf("window = %v - %v", start.In(loc), end.In(loc))
	}
	records, err := queryTrafficBuckets(db, trafficQuery{hourly: true, start: start, end: end, step: cfg.step, serverID: 1})
	if err != nil {
		t.Fatal(err)
	}
	points, _ := buildAggregatedTrafficPoints(records, start, end, cfg.step)
	if len(points) != 7 {
		t.Fatalf("points = %d", len(points))
	}
	if points[6].Upload != 7 || points[5].Upload != 3 {
		t.Fatalf("daily split wrong: oct2=%d oct3=%d", points[5].Upload, points[6].Upload)
	}
	if !points[6].Time.Equal(time.Date(2026, 10, 3, 0, 0, 0, 0, loc)) {
		t.Fatalf("last bucket starts %v", points[6].Time.In(loc))
	}
}

func TestBackfillTrafficHourlyRebuildsFromMinuteRows(t *testing.T) {
	db := testDB(t)
	base := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	minute := []model.TrafficRecord{
		{ServerID: 1, Bucket: base.Add(time.Minute), Upload: 1, Download: 2, UploadRate: 5},
		{ServerID: 1, Bucket: base.Add(59 * time.Minute), Upload: 3, Download: 4, UploadRate: 9},
		{ServerID: 1, Bucket: base.Add(61 * time.Minute), Upload: 10},
		{ServerID: 1, InboundID: 4, Bucket: base.Add(time.Minute), Upload: 1},
	}
	if err := db.Create(&minute).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // re-running must not double count
		if err := backfillTrafficHourly(db); err != nil {
			t.Fatal(err)
		}
	}
	var hourly []model.TrafficHourly
	db.Order("inbound_id, bucket").Find(&hourly)
	if len(hourly) != 3 {
		t.Fatalf("hourly = %+v", hourly)
	}
	if !hourly[0].Bucket.Equal(base) || hourly[0].Upload != 4 || hourly[0].Download != 6 || hourly[0].UploadRate != 9 {
		t.Fatalf("first hour = %+v", hourly[0])
	}
	if hourly[1].Upload != 10 || hourly[2].InboundID != 4 {
		t.Fatalf("rollup = %+v", hourly)
	}
}

func TestPruneTrafficKeepsHourlyBeyondMinuteRetention(t *testing.T) {
	db := testDB(t)
	now := time.Now().UTC()
	old := now.Add(-10 * 24 * time.Hour).Truncate(time.Hour)
	db.Create(&model.TrafficRecord{ServerID: 1, Bucket: old, Upload: 1})
	db.Create(&model.TrafficHourly{ServerID: 1, Bucket: old, Upload: 1})
	db.Create(&model.TrafficHourly{ServerID: 1, Bucket: now.Add(-500 * 24 * time.Hour).Truncate(time.Hour), Upload: 1})
	if err := pruneTrafficRecords(db, now); err != nil {
		t.Fatal(err)
	}
	var minuteCount, hourlyCount int64
	db.Model(&model.TrafficRecord{}).Count(&minuteCount)
	db.Model(&model.TrafficHourly{}).Count(&hourlyCount)
	if minuteCount != 0 || hourlyCount != 1 {
		t.Fatalf("minute=%d hourly=%d, want 0/1", minuteCount, hourlyCount)
	}
}

func TestSubscriptionClient(t *testing.T) {
	for ua, want := range map[string]string{
		"Shadowrocket/2070 CFNetwork/1568":      "Shadowrocket",
		"clash-verge/v1.7.7":                    "Clash Verge",
		"mihomo/1.18.9":                         "mihomo",
		"ClashMetaForAndroid/2.10.1.Meta":       "Clash Meta",
		"SFA/1.10.0 (Android; sing-box 1.10.0)": "sing-box",
		"Surge iOS/3099":                        "Surge",
		"":                                      "未知",
		"Something/1.0":                         "其他",
	} {
		if got := subscriptionClient(ua); got != want {
			t.Errorf("subscriptionClient(%q) = %q, want %q", ua, got, want)
		}
	}
}

func TestSubscriptionFetchIsRecorded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	user := model.User{Email: "fetcher", Role: model.RoleUser, Enabled: true, SubToken: "fetch-token", ProxyToken: "seed"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	app := &App{db: db}
	for _, ip := range []string{"203.0.113.1", "203.0.113.2", "203.0.113.1"} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/sub/fetch-token?target=clash", nil)
		c.Request.RemoteAddr = ip + ":1234"
		c.Request.Header.Set("User-Agent", "mihomo/1.18")
		app.recordSubscriptionFetch(c, &user, "clash", http.StatusOK)
	}
	if err := db.First(&user, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if user.SubFetchCount != 3 || user.LastSubClient != "mihomo" || user.LastSubAt == nil || user.LastSubIP == "" {
		t.Fatalf("user usage = count %d client %q at %v ip %q", user.SubFetchCount, user.LastSubClient, user.LastSubAt, user.LastSubIP)
	}
	activity, err := subscriptionActivitySince(db, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got := activity[user.ID]; got.Fetches != 3 || got.IPs != 2 {
		t.Fatalf("activity = %+v", got)
	}
	if err := pruneSubscriptionFetches(db, time.Now().Add(31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var remaining int64
	db.Model(&model.SubscriptionFetch{}).Count(&remaining)
	if remaining != 0 {
		t.Fatalf("prune left %d rows", remaining)
	}
}
