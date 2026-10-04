package panel

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

const (
	// Minute buckets serve short live-ish ranges; hourly rollups serve
	// everything else and are kept long enough for yearly comparisons.
	trafficMinuteRetention = 3 * 24 * time.Hour
	trafficHourlyRetention = 400 * 24 * time.Hour
	trafficStorageBucket   = time.Minute
	trafficHourlyBucket    = time.Hour
	maxTrafficQueryRows    = 100000
	// trafficFlushInterval bounds how much accounted traffic a crash can lose
	// while keeping database writes independent of the 3s Agent report rate.
	trafficFlushInterval = 15 * time.Second
	// maxAgentClockSkew is how far an Agent's sample time may drift from the
	// panel clock before the panel's own receive time is used instead.
	maxAgentClockSkew = time.Minute
)

type trafficRangeConfig struct {
	duration time.Duration
	step     time.Duration
	// hourly reads the hourly rollup table instead of minute buckets.
	hourly bool
}

var trafficRanges = map[string]trafficRangeConfig{
	"15m": {duration: 15 * time.Minute, step: time.Minute},
	"30m": {duration: 30 * time.Minute, step: 2 * time.Minute},
	"1h":  {duration: time.Hour, step: 2 * time.Minute},
	"12h": {duration: 12 * time.Hour, step: 30 * time.Minute},
	"24h": {duration: 24 * time.Hour, step: time.Hour, hourly: true},
	"7d":  {duration: 7 * 24 * time.Hour, step: 24 * time.Hour, hourly: true},
	"30d": {duration: 30 * 24 * time.Hour, step: 24 * time.Hour, hourly: true},
}

func trafficDelta(current, previous uint64) uint64 {
	if current >= previous {
		return current - previous
	}
	return current
}

func maxUint64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

// ---------- write path ----------

type trafficBucketKey struct {
	ServerID  uint
	InboundID uint
	Bucket    time.Time
}

type trafficBucketValue struct {
	Upload, Download         uint64
	UploadRate, DownloadRate uint64
	TCPConnections           int
	UDPConnections           int
}

func (v trafficBucketValue) empty() bool {
	return v.Upload == 0 && v.Download == 0 && v.UploadRate == 0 && v.DownloadRate == 0 &&
		v.TCPConnections == 0 && v.UDPConnections == 0
}

// merge folds a newer sample into an existing bucket: bytes add up, rates
// keep the maximum, and connection counts keep the latest observation.
func (v *trafficBucketValue) merge(next trafficBucketValue) {
	v.Upload += next.Upload
	v.Download += next.Download
	v.UploadRate = maxUint64(v.UploadRate, next.UploadRate)
	v.DownloadRate = maxUint64(v.DownloadRate, next.DownloadRate)
	v.TCPConnections = next.TCPConnections
	v.UDPConnections = next.UDPConnections
}

type trafficBucketDelta struct {
	Key   trafficBucketKey
	Value trafficBucketValue
}

// trafficWriter buffers bucket deltas in memory and writes them in one
// transaction per flush. Node counters (the accounting baseline) are still
// persisted on every report, so a crash loses at most one flush interval of
// history but never double-counts.
type trafficWriter struct {
	db      *gorm.DB
	mu      sync.Mutex
	pending map[trafficBucketKey]trafficBucketValue
	dropped map[uint]bool
	flushMu sync.Mutex
	// Per-user hourly deltas and recent rename aliases (traffic_users.go).
	pendingUsers map[userBucketKey]userBucketValue
	aliases      map[string]userAlias
}

func newTrafficWriter(db *gorm.DB) *trafficWriter {
	return &trafficWriter{
		db: db, pending: map[trafficBucketKey]trafficBucketValue{}, dropped: map[uint]bool{},
		pendingUsers: map[userBucketKey]userBucketValue{}, aliases: map[string]userAlias{},
	}
}

func (w *trafficWriter) add(deltas []trafficBucketDelta) {
	if len(deltas) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, delta := range deltas {
		if w.dropped[delta.Key.ServerID] {
			continue
		}
		value := w.pending[delta.Key]
		value.merge(delta.Value)
		w.pending[delta.Key] = value
	}
}

// dropServer discards buffered rows for a deleted node so a later flush
// cannot recreate orphan history.
func (w *trafficWriter) dropServer(serverID uint) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for key := range w.pending {
		if key.ServerID == serverID {
			delete(w.pending, key)
		}
	}
	for key := range w.pendingUsers {
		if key.ServerID == serverID {
			delete(w.pendingUsers, key)
		}
	}
	w.dropped[serverID] = true
}

// record persists node counters immediately and buffers bucket history.
func (w *trafficWriter) record(serverID uint, snapshot *protocol.TrafficSnapshot) error {
	w.mu.Lock()
	delete(w.dropped, serverID)
	w.mu.Unlock()
	deltas, userDeltas, err := applyServerTraffic(w.db, serverID, snapshot, time.Now())
	if err != nil {
		return err
	}
	w.add(deltas)
	if len(userDeltas) > 0 {
		resolved, err := w.resolveUserDeltas(userDeltas, time.Now())
		if err != nil {
			return err
		}
		w.addUsers(resolved)
	}
	return nil
}

func (w *trafficWriter) flush() error {
	w.flushMu.Lock()
	defer w.flushMu.Unlock()
	w.mu.Lock()
	pending := w.pending
	w.pending = map[trafficBucketKey]trafficBucketValue{}
	pendingUsers := w.pendingUsers
	w.pendingUsers = map[userBucketKey]userBucketValue{}
	w.mu.Unlock()
	if err := writeUserTrafficBuckets(w.db, pendingUsers); err != nil {
		w.restoreUsers(pendingUsers)
		// Node and port history below is independent; still write it.
		log.Printf("traffic: flush per-user history: %v", err)
	}
	if len(pending) == 0 {
		return nil
	}
	deltas := make([]trafficBucketDelta, 0, len(pending))
	for key, value := range pending {
		deltas = append(deltas, trafficBucketDelta{Key: key, Value: value})
	}
	if err := writeTrafficBuckets(w.db, deltas); err != nil {
		// Put the data back so the next flush retries it.
		w.add(deltas)
		return err
	}
	return nil
}

func (w *trafficWriter) run(ctx context.Context) {
	ticker := time.NewTicker(trafficFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.flush(); err != nil {
				log.Printf("traffic: flush history: %v", err)
			}
		}
	}
}

// recordServerTraffic applies one report and writes its history immediately.
func recordServerTraffic(db *gorm.DB, serverID uint, snapshot *protocol.TrafficSnapshot) error {
	deltas, _, err := applyServerTraffic(db, serverID, snapshot, time.Now())
	if err != nil {
		return err
	}
	return writeTrafficBuckets(db, deltas)
}

// trafficSampleTime trusts the Agent clock only when it agrees with ours.
func trafficSampleTime(sampledAt int64, now time.Time) time.Time {
	now = now.UTC()
	if sampledAt <= 0 {
		return now
	}
	t := time.Unix(sampledAt, 0).UTC()
	if t.Before(now.Add(-maxAgentClockSkew)) || t.After(now.Add(maxAgentClockSkew)) {
		return now
	}
	return t
}

// applyServerTraffic updates the node counters and returns the bucket deltas
// this report contributes, plus per-user deltas (still keyed by the proxy
// user name) for multi-user inbounds.
func applyServerTraffic(db *gorm.DB, serverID uint, snapshot *protocol.TrafficSnapshot, now time.Time) ([]trafficBucketDelta, []userTrafficDelta, error) {
	if snapshot == nil {
		return nil, nil, db.Model(&model.Server{}).Where("id = ?", serverID).Updates(map[string]any{
			"traffic_available":       false,
			"traffic_upload_rate":     0,
			"traffic_download_rate":   0,
			"traffic_tcp_connections": 0,
			"traffic_udp_connections": 0,
		}).Error
	}

	var deltas []trafficBucketDelta
	var userDeltas []userTrafficDelta
	err := db.Transaction(func(tx *gorm.DB) error {
		var server model.Server
		if err := tx.Select(
			"id", "traffic_available", "traffic_upload", "traffic_download",
			"traffic_remote_upload", "traffic_remote_download", "traffic_updated_at", "config_mode",
		).First(&server, serverID).Error; err != nil {
			return err
		}

		// The first successful sample establishes a baseline. This prevents old
		// traffic from before the feature was enabled being counted as new usage.
		uploadDelta := uint64(0)
		downloadDelta := uint64(0)
		// Availability can briefly become false when the local API times out.
		// The last successful sample still provides a valid accounting baseline.
		hasBaseline := server.TrafficAvailable || server.TrafficUpdatedAt != nil
		if hasBaseline {
			uploadDelta = trafficDelta(snapshot.UploadTotal, server.TrafficRemoteUpload)
			downloadDelta = trafficDelta(snapshot.DownloadTotal, server.TrafficRemoteDownload)
		}
		sampledAt := trafficSampleTime(snapshot.SampledAt, now)
		bucket := sampledAt.Truncate(trafficStorageBucket)

		updates := map[string]any{
			"traffic_available":       true,
			"traffic_upload":          server.TrafficUpload + uploadDelta,
			"traffic_download":        server.TrafficDownload + downloadDelta,
			"traffic_upload_rate":     snapshot.UploadRate,
			"traffic_download_rate":   snapshot.DownloadRate,
			"traffic_tcp_connections": snapshot.TCPConnections,
			"traffic_udp_connections": snapshot.UDPConnections,
			"traffic_updated_at":      sampledAt,
			"traffic_remote_upload":   snapshot.UploadTotal,
			"traffic_remote_download": snapshot.DownloadTotal,
		}
		if err := tx.Model(&model.Server{}).Where("id = ?", serverID).Updates(updates).Error; err != nil {
			return err
		}
		deltas = append(deltas, trafficBucketDelta{
			Key: trafficBucketKey{ServerID: serverID, Bucket: bucket},
			Value: trafficBucketValue{
				Upload: uploadDelta, Download: downloadDelta,
				UploadRate:     maxUint64(snapshot.UploadRate, snapshot.PeakUploadRate),
				DownloadRate:   maxUint64(snapshot.DownloadRate, snapshot.PeakDownloadRate),
				TCPConnections: snapshot.TCPConnections, UDPConnections: snapshot.UDPConnections,
			},
		})

		// Without a baseline the node total for this report is zero, so the
		// per-inbound deltas (which the Agent accumulated meanwhile) must be
		// dropped too; otherwise ports would exceed the node total.
		if !hasBaseline || (len(snapshot.Ports) == 0 && len(snapshot.Users) == 0) {
			return nil
		}
		var inbounds []model.Inbound
		if err := tx.Select("id", "tag", "type", "settings").Where("server_id = ?", serverID).Find(&inbounds).Error; err != nil {
			return err
		}
		inboundIDs := make(map[string]uint, len(inbounds))
		for _, inbound := range inbounds {
			inboundIDs[inbound.Tag] = inbound.ID
		}
		for _, port := range snapshot.Ports {
			inboundID := inboundIDs[port.Inbound]
			if inboundID == 0 {
				continue
			}
			deltas = append(deltas, trafficBucketDelta{
				Key: trafficBucketKey{ServerID: serverID, InboundID: inboundID, Bucket: bucket},
				Value: trafficBucketValue{
					Upload: port.Upload, Download: port.Download,
					UploadRate: port.UploadRate, DownloadRate: port.DownloadRate,
				},
			})
		}
		// Raw-mode configs carry user names the panel did not issue, which could
		// match panel users by accident; only managed configs are attributed.
		if server.ConfigMode != model.ConfigModeRaw {
			userDeltas = multiUserTrafficDeltas(serverID, inbounds, snapshot.Users, sampledAt.Truncate(trafficHourlyBucket))
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return deltas, userDeltas, nil
}

// trafficUpsertSet builds the conflict update for one bucket table. Bytes add
// up, rates keep the maximum, and connection counts either keep the latest
// (minute buckets) or the maximum (hourly rollups).
func trafficUpsertSet(db *gorm.DB, table string, connectionsMax bool) clause.Set {
	mysql := db.Dialector.Name() == "mysql"
	add := func(column string) clause.Assignment {
		expr := fmt.Sprintf("%s.%s + excluded.%s", table, column, column)
		if mysql {
			expr = fmt.Sprintf("%s + VALUES(%s)", column, column)
		}
		return clause.Assignment{Column: clause.Column{Name: column}, Value: gorm.Expr(expr)}
	}
	greatest := func(column string) clause.Assignment {
		expr := fmt.Sprintf("CASE WHEN excluded.%s > %s.%s THEN excluded.%s ELSE %s.%s END", column, table, column, column, table, column)
		if mysql {
			expr = fmt.Sprintf("GREATEST(%s, VALUES(%s))", column, column)
		}
		return clause.Assignment{Column: clause.Column{Name: column}, Value: gorm.Expr(expr)}
	}
	latest := func(column string) clause.Assignment {
		expr := "excluded." + column
		if mysql {
			expr = fmt.Sprintf("VALUES(%s)", column)
		}
		return clause.Assignment{Column: clause.Column{Name: column}, Value: gorm.Expr(expr)}
	}
	connections := latest
	if connectionsMax {
		connections = greatest
	}
	return clause.Set{
		add("upload"), add("download"),
		greatest("upload_rate"), greatest("download_rate"),
		connections("tcp_connections"), connections("udp_connections"),
		latest("updated_at"),
	}
}

var trafficConflictColumns = []clause.Column{{Name: "server_id"}, {Name: "inbound_id"}, {Name: "bucket"}}

// writeTrafficBuckets upserts minute buckets and their hourly rollups.
func writeTrafficBuckets(db *gorm.DB, deltas []trafficBucketDelta) error {
	minute := make([]model.TrafficRecord, 0, len(deltas))
	hourlyByKey := map[trafficBucketKey]trafficBucketValue{}
	for _, delta := range deltas {
		if delta.Value.empty() {
			continue
		}
		v := delta.Value
		minute = append(minute, model.TrafficRecord{
			ServerID: delta.Key.ServerID, InboundID: delta.Key.InboundID, Bucket: delta.Key.Bucket.UTC(),
			Upload: v.Upload, Download: v.Download, UploadRate: v.UploadRate, DownloadRate: v.DownloadRate,
			TCPConnections: v.TCPConnections, UDPConnections: v.UDPConnections,
		})
		hourKey := delta.Key
		hourKey.Bucket = delta.Key.Bucket.UTC().Truncate(trafficHourlyBucket)
		hour := hourlyByKey[hourKey]
		hour.Upload += v.Upload
		hour.Download += v.Download
		hour.UploadRate = maxUint64(hour.UploadRate, v.UploadRate)
		hour.DownloadRate = maxUint64(hour.DownloadRate, v.DownloadRate)
		if v.TCPConnections > hour.TCPConnections {
			hour.TCPConnections = v.TCPConnections
		}
		if v.UDPConnections > hour.UDPConnections {
			hour.UDPConnections = v.UDPConnections
		}
		hourlyByKey[hourKey] = hour
	}
	if len(minute) == 0 {
		return nil
	}
	hourly := make([]model.TrafficHourly, 0, len(hourlyByKey))
	for key, v := range hourlyByKey {
		hourly = append(hourly, model.TrafficHourly{
			ServerID: key.ServerID, InboundID: key.InboundID, Bucket: key.Bucket,
			Upload: v.Upload, Download: v.Download, UploadRate: v.UploadRate, DownloadRate: v.DownloadRate,
			TCPConnections: v.TCPConnections, UDPConnections: v.UDPConnections,
		})
	}
	// Deterministic order keeps lock acquisition stable across databases.
	sort.Slice(minute, func(i, j int) bool {
		return trafficRowLess(minute[i].ServerID, minute[i].InboundID, minute[i].Bucket, minute[j].ServerID, minute[j].InboundID, minute[j].Bucket)
	})
	sort.Slice(hourly, func(i, j int) bool {
		return trafficRowLess(hourly[i].ServerID, hourly[i].InboundID, hourly[i].Bucket, hourly[j].ServerID, hourly[j].InboundID, hourly[j].Bucket)
	})

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{Columns: trafficConflictColumns, DoUpdates: trafficUpsertSet(tx, "traffic_records", false)}).
			CreateInBatches(&minute, 200).Error; err != nil {
			return fmt.Errorf("upsert minute buckets: %w", err)
		}
		if err := tx.Clauses(clause.OnConflict{Columns: trafficConflictColumns, DoUpdates: trafficUpsertSet(tx, "traffic_hourly", true)}).
			CreateInBatches(&hourly, 200).Error; err != nil {
			return fmt.Errorf("upsert hourly buckets: %w", err)
		}
		return nil
	})
}

func trafficRowLess(aServer, aInbound uint, aBucket time.Time, bServer, bInbound uint, bBucket time.Time) bool {
	if aServer != bServer {
		return aServer < bServer
	}
	if aInbound != bInbound {
		return aInbound < bInbound
	}
	return aBucket.Before(bBucket)
}

// ---------- read path ----------

type trafficPoint struct {
	Time           time.Time `json:"time"`
	Upload         uint64    `json:"upload"`
	Download       uint64    `json:"download"`
	UploadRate     uint64    `json:"upload_rate"`
	DownloadRate   uint64    `json:"download_rate"`
	TCPConnections int       `json:"tcp_connections"`
	UDPConnections int       `json:"udp_connections"`
}

type trafficPortSeries struct {
	InboundID uint           `json:"inbound_id"`
	Tag       string         `json:"tag"`
	Port      int            `json:"port"`
	Type      string         `json:"type"`
	Upload    uint64         `json:"upload"`
	Download  uint64         `json:"download"`
	Points    []trafficPoint `json:"points"`
}

type trafficSeries struct {
	Available        bool                `json:"available"`
	Range            string              `json:"range"`
	StepSeconds      int64               `json:"step_seconds"`
	Timezone         string              `json:"timezone"`
	UpdatedAt        *time.Time          `json:"updated_at"`
	Upload           uint64              `json:"upload"`
	Download         uint64              `json:"download"`
	PeakUploadRate   uint64              `json:"peak_upload_rate"`
	PeakDownloadRate uint64              `json:"peak_download_rate"`
	UploadRate       uint64              `json:"upload_rate"`
	DownloadRate     uint64              `json:"download_rate"`
	TCPConnections   int                 `json:"tcp_connections"`
	UDPConnections   int                 `json:"udp_connections"`
	Points           []trafficPoint      `json:"points"`
	Ports            []trafficPortSeries `json:"ports"`
	// Unattributed is node traffic that could not be assigned to a known
	// inbound: inbounds managed outside the panel, and (on nodes without the
	// sing-box 1.14+ API service) connections that closed between samples.
	UnattributedUpload   uint64 `json:"unattributed_upload"`
	UnattributedDownload uint64 `json:"unattributed_download"`
}

// trafficWindow returns the [start, end) window for a range. Daily ranges
// align to midnight in loc so each bar is one local calendar day.
func trafficWindow(now time.Time, config trafficRangeConfig, loc *time.Location) (start, end time.Time) {
	if loc == nil {
		loc = time.UTC
	}
	if config.step >= 24*time.Hour {
		local := now.In(loc)
		end = time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc).UTC()
		days := int(config.duration / (24 * time.Hour))
		start = time.Date(local.Year(), local.Month(), local.Day()+1-days, 0, 0, 0, 0, loc).UTC()
		return start, end
	}
	end = now.UTC().Truncate(config.step).Add(config.step)
	start = end.Add(-config.duration)
	return start, end
}

// stepOffsetSeconds is the shift that aligns step-sized buckets to local
// midnight. Zero for sub-day steps.
func stepOffsetSeconds(step time.Duration, start time.Time) int64 {
	if step < 24*time.Hour {
		return 0
	}
	stepSeconds := int64(step / time.Second)
	offset := (stepSeconds - start.Unix()%stepSeconds) % stepSeconds
	return offset
}

func emptyTrafficPoints(start, end time.Time, step time.Duration) []trafficPoint {
	count := int(end.Sub(start) / step)
	if count < 0 {
		count = 0
	}
	points := make([]trafficPoint, count)
	for i := range points {
		points[i].Time = start.Add(time.Duration(i) * step)
	}
	return points
}

func sumTraffic(points []trafficPoint) (upload, download uint64) {
	for _, point := range points {
		upload += point.Upload
		download += point.Download
	}
	return upload, download
}

func peakRates(points []trafficPoint) (upload, download uint64) {
	for _, point := range points {
		upload = maxUint64(upload, point.UploadRate)
		download = maxUint64(download, point.DownloadRate)
	}
	return upload, download
}

type aggregatedTrafficRecord struct {
	ServerID       uint  `gorm:"column:server_id"`
	InboundID      uint  `gorm:"column:inbound_id"`
	BucketIndex    int64 `gorm:"column:bucket_index"`
	Upload         uint64
	Download       uint64
	UploadRate     uint64
	DownloadRate   uint64
	TCPConnections int
	UDPConnections int
}

func trafficBucketExpression(db *gorm.DB, stepSeconds, offsetSeconds int64) (string, error) {
	step := strconv.FormatInt(stepSeconds, 10)
	offset := strconv.FormatInt(offsetSeconds, 10)
	switch db.Dialector.Name() {
	case "sqlite":
		return "(CAST(strftime('%s', bucket) AS INTEGER) + " + offset + ") / " + step, nil
	case "mysql":
		return "FLOOR((UNIX_TIMESTAMP(bucket) + " + offset + ") / " + step + ")", nil
	case "postgres":
		return "FLOOR((EXTRACT(EPOCH FROM bucket) + " + offset + ") / " + step + ")", nil
	default:
		return "", fmt.Errorf("unsupported database driver %q", db.Dialector.Name())
	}
}

type trafficQuery struct {
	hourly   bool
	start    time.Time
	end      time.Time
	step     time.Duration
	serverID uint // 0 = every server
	// totalsOnly restricts the query to node totals (inbound_id = 0).
	totalsOnly bool
}

func (q trafficQuery) model() any {
	if q.hourly {
		return &model.TrafficHourly{}
	}
	return &model.TrafficRecord{}
}

func queryTrafficBuckets(db *gorm.DB, q trafficQuery) ([]aggregatedTrafficRecord, error) {
	stepSeconds := int64(q.step / time.Second)
	if stepSeconds <= 0 {
		return nil, fmt.Errorf("invalid traffic aggregation step")
	}
	offset := stepOffsetSeconds(q.step, q.start)
	bucketExpression, err := trafficBucketExpression(db, stepSeconds, offset)
	if err != nil {
		return nil, err
	}
	selectExpression := "server_id, inbound_id, " + bucketExpression + " AS bucket_index, " +
		"SUM(upload) AS upload, SUM(download) AS download, " +
		"MAX(upload_rate) AS upload_rate, MAX(download_rate) AS download_rate, " +
		"MAX(tcp_connections) AS tcp_connections, MAX(udp_connections) AS udp_connections"
	query := db.Model(q.model()).Select(selectExpression).Where("bucket >= ? AND bucket < ?", q.start, q.end)
	if q.serverID != 0 {
		query = query.Where("server_id = ?", q.serverID)
	}
	if q.totalsOnly {
		query = query.Where("inbound_id = 0")
	}
	var rows []aggregatedTrafficRecord
	if err := query.
		Group("server_id, inbound_id, " + bucketExpression).
		Order("bucket_index, server_id, inbound_id").
		Limit(maxTrafficQueryRows + 1).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > maxTrafficQueryRows {
		return nil, fmt.Errorf("traffic query produced too many aggregated rows")
	}
	// Convert bucket indices back to real bucket start times.
	for i := range rows {
		rows[i].BucketIndex = rows[i].BucketIndex*stepSeconds - offset
	}
	return rows, nil
}

// aggregateTrafficRecords groups one server's minute buckets.
func aggregateTrafficRecords(db *gorm.DB, serverID uint, start, end time.Time, step time.Duration) ([]aggregatedTrafficRecord, error) {
	return queryTrafficBuckets(db, trafficQuery{start: start, end: end, step: step, serverID: serverID})
}

func trafficPointIndex(bucketUnix int64, start, end time.Time, step time.Duration, count int) int {
	bucket := time.Unix(bucketUnix, 0).UTC()
	if bucket.Before(start) || !bucket.Before(end) {
		return -1
	}
	index := int(bucket.Sub(start) / step)
	if index < 0 || index >= count {
		return -1
	}
	return index
}

// buildAggregatedTrafficPoints turns one server's rows into the node total
// series and per-inbound series.
func buildAggregatedTrafficPoints(records []aggregatedTrafficRecord, start, end time.Time, step time.Duration) ([]trafficPoint, map[uint][]trafficPoint) {
	total := emptyTrafficPoints(start, end, step)
	byInbound := make(map[uint][]trafficPoint)
	for _, record := range records {
		index := trafficPointIndex(record.BucketIndex, start, end, step, len(total))
		if index < 0 {
			continue
		}
		points := total
		if record.InboundID != 0 {
			points = byInbound[record.InboundID]
			if points == nil {
				points = emptyTrafficPoints(start, end, step)
				byInbound[record.InboundID] = points
			}
		}
		points[index].Upload = record.Upload
		points[index].Download = record.Download
		points[index].UploadRate = record.UploadRate
		points[index].DownloadRate = record.DownloadRate
		points[index].TCPConnections = record.TCPConnections
		points[index].UDPConnections = record.UDPConnections
	}
	return total, byInbound
}

// location returns the timezone used for traffic day/month boundaries.
func (a *App) location() *time.Location {
	return a.cfg.Location()
}

func (a *App) parseTrafficRange(c *gin.Context) (string, trafficRangeConfig, bool) {
	rangeName := c.DefaultQuery("range", "24h")
	rangeConfig, exists := trafficRanges[rangeName]
	if !exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unsupported range %q", rangeName)})
		return "", trafficRangeConfig{}, false
	}
	return rangeName, rangeConfig, true
}

func (a *App) serverTraffic(c *gin.Context) {
	serverID, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var server model.Server
	if err := a.db.Select(
		"id", "traffic_available", "traffic_updated_at", "traffic_upload_rate", "traffic_download_rate",
		"traffic_tcp_connections", "traffic_udp_connections",
	).First(&server, serverID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	rangeName, rangeConfig, ok := a.parseTrafficRange(c)
	if !ok {
		return
	}
	a.flushTraffic()
	loc := a.location()
	start, end := trafficWindow(time.Now(), rangeConfig, loc)
	records, err := queryTrafficBuckets(a.db, trafficQuery{
		hourly: rangeConfig.hourly, start: start, end: end, step: rangeConfig.step, serverID: serverID,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	totalPoints, byInbound := buildAggregatedTrafficPoints(records, start, end, rangeConfig.step)
	totalUpload, totalDownload := sumTraffic(totalPoints)
	peakUpload, peakDownload := peakRates(totalPoints)

	var inbounds []model.Inbound
	if err := a.db.Where("server_id = ?", serverID).Order("listen_port, id").Find(&inbounds).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ports := make([]trafficPortSeries, 0, len(inbounds))
	var portUpload, portDownload uint64
	for _, inbound := range inbounds {
		points := byInbound[inbound.ID]
		if points == nil {
			points = emptyTrafficPoints(start, end, rangeConfig.step)
		}
		upload, download := sumTraffic(points)
		portUpload += upload
		portDownload += download
		ports = append(ports, trafficPortSeries{
			InboundID: inbound.ID, Tag: inbound.Tag, Port: inbound.ListenPort, Type: string(inbound.Type),
			Upload: upload, Download: download, Points: points,
		})
	}
	// Rows for deleted inbounds still count towards the attributed share.
	for inboundID, points := range byInbound {
		known := false
		for _, inbound := range inbounds {
			if inbound.ID == inboundID {
				known = true
				break
			}
		}
		if !known {
			upload, download := sumTraffic(points)
			portUpload += upload
			portDownload += download
		}
	}
	var unattributedUpload, unattributedDownload uint64
	if totalUpload > portUpload {
		unattributedUpload = totalUpload - portUpload
	}
	if totalDownload > portDownload {
		unattributedDownload = totalDownload - portDownload
	}

	c.JSON(http.StatusOK, trafficSeries{
		Available: server.TrafficAvailable,
		Range:     rangeName, StepSeconds: int64(rangeConfig.step / time.Second), Timezone: loc.String(),
		UpdatedAt: server.TrafficUpdatedAt,
		Upload:    totalUpload, Download: totalDownload,
		PeakUploadRate: peakUpload, PeakDownloadRate: peakDownload,
		UploadRate: server.TrafficUploadRate, DownloadRate: server.TrafficDownloadRate,
		TCPConnections: server.TrafficTCPConnections, UDPConnections: server.TrafficUDPConnections,
		Points: totalPoints, Ports: ports,
		UnattributedUpload: unattributedUpload, UnattributedDownload: unattributedDownload,
	})
}

// ---------- retention ----------

func pruneTrafficRecords(db *gorm.DB, now time.Time) error {
	minuteCutoff := now.UTC().Add(-trafficMinuteRetention).Truncate(time.Hour)
	if err := db.Where("bucket < ?", minuteCutoff).Delete(&model.TrafficRecord{}).Error; err != nil {
		return err
	}
	hourlyCutoff := now.UTC().Add(-trafficHourlyRetention).Truncate(24 * time.Hour)
	if err := db.Where("bucket < ?", hourlyCutoff).Delete(&model.TrafficHourly{}).Error; err != nil {
		return err
	}
	return db.Where("bucket < ?", hourlyCutoff).Delete(&model.TrafficUserHourly{}).Error
}

// backfillTrafficHourly rebuilds hourly rollups from existing minute rows.
// It replaces any existing rollup rows so it is safe to re-run.
func backfillTrafficHourly(tx *gorm.DB) error {
	if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.TrafficHourly{}).Error; err != nil {
		return err
	}
	const step = int64(3600)
	bucketExpression, err := trafficBucketExpression(tx, step, 0)
	if err != nil {
		return err
	}
	var rows []aggregatedTrafficRecord
	if err := tx.Model(&model.TrafficRecord{}).
		Select("server_id, inbound_id, " + bucketExpression + " AS bucket_index, " +
			"SUM(upload) AS upload, SUM(download) AS download, " +
			"MAX(upload_rate) AS upload_rate, MAX(download_rate) AS download_rate, " +
			"MAX(tcp_connections) AS tcp_connections, MAX(udp_connections) AS udp_connections").
		Group("server_id, inbound_id, " + bucketExpression).
		Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	hourly := make([]model.TrafficHourly, 0, len(rows))
	for _, row := range rows {
		hourly = append(hourly, model.TrafficHourly{
			ServerID: row.ServerID, InboundID: row.InboundID, Bucket: time.Unix(row.BucketIndex*step, 0).UTC(),
			Upload: row.Upload, Download: row.Download, UploadRate: row.UploadRate, DownloadRate: row.DownloadRate,
			TCPConnections: row.TCPConnections, UDPConnections: row.UDPConnections,
		})
	}
	return tx.CreateInBatches(&hourly, 500).Error
}
