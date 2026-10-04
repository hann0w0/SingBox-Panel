package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

type userTrafficFixture struct {
	db        *gorm.DB
	server    model.Server
	multi     model.Inbound
	single    model.Inbound
	alice     model.User
	namedUser model.User
}

func newUserTrafficFixture(t *testing.T, configMode string) userTrafficFixture {
	t.Helper()
	db := testDB(t)
	f := userTrafficFixture{db: db}
	f.server = model.Server{Name: "node-a", AgentToken: "tok-" + configMode, ConfigMode: configMode}
	if configMode == model.ConfigModeRaw {
		f.server.RawConfig = model.JSONText(`{"inbounds":[]}`)
	}
	mustCreate(t, db, &f.server)
	f.multi = model.Inbound{ServerID: f.server.ID, Tag: "vless-multi", Type: "vless", ListenPort: 443, Enabled: true,
		Settings: model.JSONText(`{"multi_user":true}`)}
	f.single = model.Inbound{ServerID: f.server.ID, Tag: "vless-single", Type: "vless", ListenPort: 8443, Enabled: true,
		Settings: model.JSONText(`{"single_user":true,"uuid":"b831381d-6324-4d53-ad4f-8cda48b30811"}`)}
	mustCreate(t, db, &f.multi)
	mustCreate(t, db, &f.single)
	normalized := "alice@example.com"
	f.alice = model.User{Email: "Alice@Example.com", EmailNormalized: &normalized, Role: model.RoleUser, Enabled: true, SubToken: "s1", ProxyToken: "p1"}
	mustCreate(t, db, &f.alice)
	// A real panel user called "user" (the single-user identity name).
	userName := "user"
	f.namedUser = model.User{Email: "user", EmailNormalized: &userName, Role: model.RoleUser, Enabled: true, SubToken: "s2", ProxyToken: "p2"}
	mustCreate(t, db, &f.namedUser)
	return f
}

func mustCreate(t *testing.T, db *gorm.DB, value any) {
	t.Helper()
	if err := db.Create(value).Error; err != nil {
		t.Fatal(err)
	}
}

// report sends a baseline then a real report through the writer and flushes.
func (f userTrafficFixture) report(t *testing.T, w *trafficWriter, users []protocol.UserTrafficSnapshot) {
	t.Helper()
	now := time.Now()
	if err := w.record(f.server.ID, &protocol.TrafficSnapshot{SampledAt: now.Unix(), UploadTotal: 1, DownloadTotal: 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.record(f.server.ID, &protocol.TrafficSnapshot{
		SampledAt: now.Unix(), UploadTotal: 1_000_000, DownloadTotal: 1_000_000,
		Ports: []protocol.PortTrafficSnapshot{{Inbound: "vless-multi", Upload: 10, Download: 10}},
		Users: users,
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.flush(); err != nil {
		t.Fatal(err)
	}
}

func userRows(t *testing.T, db *gorm.DB) map[uint][2]uint64 {
	t.Helper()
	var rows []model.TrafficUserHourly
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := map[uint][2]uint64{}
	for _, row := range rows {
		v := out[row.UserID]
		v[0] += row.Upload
		v[1] += row.Download
		out[row.UserID] = v
	}
	return out
}

func TestUserTrafficAttributesOnlyMultiUserInbounds(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	w := newTrafficWriter(f.db)
	f.report(t, w, []protocol.UserTrafficSnapshot{
		{Inbound: "vless-multi", User: "ALICE@example.com", Upload: 100, Download: 1000}, // case-insensitive
		{Inbound: "vless-multi", User: "user", Upload: 7, Download: 70},                  // a real user named "user"
		{Inbound: "vless-single", User: "user", Upload: 5, Download: 50},                 // single-user identity
		{Inbound: "vless-multi", User: lockoutProxyUserName, Upload: 9, Download: 9},
		{Inbound: "vless-multi", User: "stranger", Upload: 9, Download: 9},
		{Inbound: "unknown-tag", User: "Alice@Example.com", Upload: 9, Download: 9},
	})
	got := userRows(t, f.db)
	if got[f.alice.ID] != [2]uint64{100, 1000} || got[f.namedUser.ID] != [2]uint64{7, 70} || len(got) != 2 {
		t.Fatalf("user rows = %v", got)
	}
	// A second report for the same hour adds up.
	f.report(t, w, []protocol.UserTrafficSnapshot{{Inbound: "vless-multi", User: "alice@example.com", Upload: 1, Download: 2}})
	if got := userRows(t, f.db)[f.alice.ID]; got != [2]uint64{101, 1002} {
		t.Fatalf("after upsert = %v", got)
	}
}

func TestUserTrafficSkipsRawConfigAndMissingBaseline(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeRaw)
	w := newTrafficWriter(f.db)
	f.report(t, w, []protocol.UserTrafficSnapshot{{Inbound: "vless-multi", User: "alice@example.com", Upload: 1, Download: 1}})
	if got := userRows(t, f.db); len(got) != 0 {
		t.Fatalf("raw config attributed users: %v", got)
	}
	g := newUserTrafficFixture(t, model.ConfigModeManaged)
	w = newTrafficWriter(g.db)
	// The very first report only establishes the node baseline.
	if err := w.record(g.server.ID, &protocol.TrafficSnapshot{SampledAt: time.Now().Unix(), UploadTotal: 5, DownloadTotal: 5,
		Users: []protocol.UserTrafficSnapshot{{Inbound: "vless-multi", User: "alice@example.com", Upload: 1, Download: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := w.flush(); err != nil {
		t.Fatal(err)
	}
	if got := userRows(t, g.db); len(got) != 0 {
		t.Fatalf("pre-baseline report attributed users: %v", got)
	}
}

func TestUserTrafficLegacyNamesAndRenameAlias(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	legacy := model.User{Email: "Legacy", Role: model.RoleUser, Enabled: true, SubToken: "s3", ProxyToken: "p3"}
	mustCreate(t, f.db, &legacy)
	if err := f.db.Model(&legacy).Update("email_normalized", nil).Error; err != nil {
		t.Fatal(err)
	}
	w := newTrafficWriter(f.db)
	// Alice was renamed; the node still reports her previous name.
	renamed := "alice-new"
	if err := f.db.Model(&f.alice).Updates(map[string]any{"email": "alice-new", "email_normalized": renamed}).Error; err != nil {
		t.Fatal(err)
	}
	w.noteRename("Alice@Example.com", f.alice.ID, time.Now())
	f.report(t, w, []protocol.UserTrafficSnapshot{
		{Inbound: "vless-multi", User: "legacy", Upload: 3, Download: 30},
		{Inbound: "vless-multi", User: "Alice@Example.com", Upload: 4, Download: 40},
	})
	got := userRows(t, f.db)
	if got[legacy.ID] != [2]uint64{3, 30} || got[f.alice.ID] != [2]uint64{4, 40} {
		t.Fatalf("user rows = %v", got)
	}
	// Expired aliases no longer resolve.
	w.noteRename("old-name", f.alice.ID, time.Now().Add(-time.Hour))
	f.report(t, w, []protocol.UserTrafficSnapshot{{Inbound: "vless-multi", User: "old-name", Upload: 1, Download: 1}})
	if got := userRows(t, f.db)[f.alice.ID]; got != [2]uint64{4, 40} {
		t.Fatalf("expired alias attributed traffic: %v", got)
	}
}

func TestUserTrafficDropsDeletedUsersAndServers(t *testing.T) {
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	w := newTrafficWriter(f.db)
	if err := w.record(f.server.ID, &protocol.TrafficSnapshot{SampledAt: time.Now().Unix(), UploadTotal: 1, DownloadTotal: 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.record(f.server.ID, &protocol.TrafficSnapshot{SampledAt: time.Now().Unix(), UploadTotal: 2, DownloadTotal: 2,
		Users: []protocol.UserTrafficSnapshot{
			{Inbound: "vless-multi", User: "alice@example.com", Upload: 1, Download: 1},
			{Inbound: "vless-multi", User: "user", Upload: 1, Download: 1},
		}}); err != nil {
		t.Fatal(err)
	}
	// Alice is deleted after her report was resolved but before the flush.
	if err := f.db.Delete(&model.User{}, f.alice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := w.flush(); err != nil {
		t.Fatal(err)
	}
	if got := userRows(t, f.db); len(got) != 1 || got[f.namedUser.ID] != [2]uint64{1, 1} {
		t.Fatalf("rows after user delete = %v", got)
	}
	w.dropServer(f.server.ID)
	if err := w.record(f.server.ID, &protocol.TrafficSnapshot{SampledAt: time.Now().Unix(), UploadTotal: 3, DownloadTotal: 3,
		Users: []protocol.UserTrafficSnapshot{{Inbound: "vless-multi", User: "user", Upload: 1, Download: 1}}}); err != nil {
		t.Fatal(err)
	}
	w.dropServer(f.server.ID)
	if err := w.flush(); err != nil {
		t.Fatal(err)
	}
	if got := userRows(t, f.db)[f.namedUser.ID]; got != [2]uint64{1, 1} {
		t.Fatalf("dropped server rows were written: %v", got)
	}
}

func TestPruneTrafficCoversUserHourly(t *testing.T) {
	db := testDB(t)
	now := time.Now().UTC()
	mustCreate(t, db, &model.TrafficUserHourly{ServerID: 1, InboundID: 1, UserID: 1, Bucket: now.Add(-500 * 24 * time.Hour).Truncate(time.Hour), Upload: 1})
	mustCreate(t, db, &model.TrafficUserHourly{ServerID: 1, InboundID: 1, UserID: 1, Bucket: now.Truncate(time.Hour), Upload: 1})
	if err := pruneTrafficRecords(db, now); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&model.TrafficUserHourly{}).Count(&count)
	if count != 1 {
		t.Fatalf("user hourly rows after prune = %d", count)
	}
}

func TestUserTrafficEndpointAndPrivacy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newUserTrafficFixture(t, model.ConfigModeManaged)
	now := time.Now().UTC().Truncate(time.Hour)
	mustCreate(t, f.db, &model.TrafficUserHourly{ServerID: f.server.ID, InboundID: f.multi.ID, UserID: f.alice.ID, Bucket: now, Upload: 10, Download: 100})
	mustCreate(t, f.db, &model.TrafficUserHourly{ServerID: f.server.ID, InboundID: 9999, UserID: f.alice.ID, Bucket: now.Add(-2 * time.Hour), Upload: 1, Download: 2})
	app := &App{db: f.db, cfg: config.PanelConfig{Timezone: "UTC"}}

	call := func(rangeName string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/admin/users/x/traffic?range="+rangeName, nil)
		c.Params = gin.Params{{Key: "id", Value: jsonNumber(f.alice.ID)}}
		app.userTraffic(c)
		return rec
	}
	rec := call("24h")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var series userTrafficSeries
	if err := json.Unmarshal(rec.Body.Bytes(), &series); err != nil {
		t.Fatal(err)
	}
	if series.Upload != 11 || series.Download != 102 || len(series.Points) != 24 || len(series.Inbounds) != 2 {
		t.Fatalf("series = %+v", series)
	}
	first, second := series.Inbounds[0], series.Inbounds[1]
	if first.InboundID != f.multi.ID || first.Tag != "vless-multi" || first.ServerName != "node-a" || first.Deleted ||
		second.InboundID != 9999 || !second.Deleted {
		t.Fatalf("inbounds = %+v", series.Inbounds)
	}
	if rec := call("1h"); rec.Code != http.StatusBadRequest {
		t.Fatalf("minute range accepted for user traffic: %d", rec.Code)
	}
	// The User model (served by /api/user/me) never carries traffic.
	raw, _ := json.Marshal(f.alice)
	if strings.Contains(string(raw), "traffic") {
		t.Fatalf("user JSON leaks traffic: %s", raw)
	}
}

func jsonNumber(id uint) string {
	raw, _ := json.Marshal(id)
	return string(raw)
}

func TestReservedProxyUserNameRejected(t *testing.T) {
	if _, _, err := validateUsername(" __SINGBOX_PANEL_DISABLED__ "); err == nil {
		t.Fatal("lockout identity name must be reserved")
	}
}

func TestLiveTrafficStreamOmitsUsers(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "n", AgentToken: "live"}
	mustCreate(t, db, &srv)
	hub := NewHub(db)
	conn := hub.register(srv.ID, "", nil)
	env, err := protocol.NewEnvelope(protocol.EvtTraffic, "", protocol.TrafficEvt{Traffic: &protocol.TrafficSnapshot{
		SampledAt: time.Now().Unix(), Users: []protocol.UserTrafficSnapshot{{Inbound: "in", User: "alice", Upload: 1}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	hub.handleEvent(conn, env)
	sub, backlog := hub.live.subscribe(srv.ID)
	defer hub.live.unsubscribe(srv.ID, sub)
	if len(backlog) == 0 {
		t.Fatal("traffic event was not published")
	}
	for _, event := range backlog {
		raw, _ := json.Marshal(event)
		if strings.Contains(string(raw), "alice") {
			t.Fatalf("live backlog carries user deltas: %s", raw)
		}
	}
}
