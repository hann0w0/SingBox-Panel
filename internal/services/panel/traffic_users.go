package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

// Per-user traffic: the Agent reports (inbound tag, proxy user name) byte
// deltas read from the sing-box API service. Only multi-user inbounds issue
// one identity per panel user, so only those are attributed; single-user
// inbounds stay in their per-port totals.

// lockoutProxyUserName is the placeholder identity of a multi-user inbound
// without any authorized user (see proxyUsersForInbound).
const lockoutProxyUserName = "__singbox_panel_disabled__"

// renameAliasTTL keeps a renamed user's previous proxy name resolvable while
// nodes still run the config issued before the rename.
const renameAliasTTL = 15 * time.Minute

type userTrafficDelta struct {
	ServerID  uint
	InboundID uint
	Name      string
	Bucket    time.Time
	Upload    uint64
	Download  uint64
}

type userBucketKey struct {
	ServerID  uint
	InboundID uint
	UserID    uint
	Bucket    time.Time
}

type userBucketValue struct {
	Upload, Download uint64
}

type userAlias struct {
	userID  uint
	expires time.Time
}

// multiUserTrafficDeltas keeps the user deltas that belong to multi-user
// inbounds of this server.
func multiUserTrafficDeltas(serverID uint, inbounds []model.Inbound, users []protocol.UserTrafficSnapshot, bucket time.Time) []userTrafficDelta {
	if len(users) == 0 {
		return nil
	}
	multiUser := make(map[string]uint, len(inbounds))
	for _, inbound := range inbounds {
		var settings singbox.InboundSettings
		if len(inbound.Settings) > 0 && json.Unmarshal(inbound.Settings, &settings) != nil {
			continue
		}
		if settings.UseMultiUser(string(inbound.Type)) {
			multiUser[inbound.Tag] = inbound.ID
		}
	}
	var out []userTrafficDelta
	for _, user := range users {
		inboundID := multiUser[user.Inbound]
		if inboundID == 0 || user.User == "" || user.User == lockoutProxyUserName {
			continue
		}
		if user.Upload == 0 && user.Download == 0 {
			continue
		}
		out = append(out, userTrafficDelta{
			ServerID: serverID, InboundID: inboundID, Name: user.User, Bucket: bucket,
			Upload: user.Upload, Download: user.Download,
		})
	}
	return out
}

// noteRename keeps the previous proxy name of a renamed user resolvable for a
// short while; nodes report the old name until the new config is applied.
func (w *trafficWriter) noteRename(previousName string, userID uint, now time.Time) {
	name := normalizeUsername(previousName)
	if name == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for key, alias := range w.aliases {
		if now.After(alias.expires) {
			delete(w.aliases, key)
		}
	}
	w.aliases[name] = userAlias{userID: userID, expires: now.Add(renameAliasTTL)}
}

// resolveUserDeltas maps proxy user names to panel user IDs. Current names
// win over rename aliases; unknown names are dropped.
func (w *trafficWriter) resolveUserDeltas(deltas []userTrafficDelta, now time.Time) (map[userBucketKey]userBucketValue, error) {
	names := make([]string, 0, len(deltas))
	seen := map[string]bool{}
	for _, delta := range deltas {
		name := normalizeUsername(delta.Name)
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	ids := make(map[string]uint, len(names))
	if len(names) > 0 {
		var users []model.User
		if err := w.db.Select("id", "email", "email_normalized").
			Where("email_normalized IN ? OR (email_normalized IS NULL AND LOWER(email) IN ?)", names, names).
			Find(&users).Error; err != nil {
			return nil, fmt.Errorf("resolve traffic users: %w", err)
		}
		for _, user := range users {
			name := normalizeUsername(user.Email)
			if user.EmailNormalized != nil {
				name = *user.EmailNormalized
			}
			ids[name] = user.ID
		}
	}
	w.mu.Lock()
	for _, name := range names {
		if _, found := ids[name]; found {
			continue
		}
		if alias, ok := w.aliases[name]; ok && now.Before(alias.expires) {
			ids[name] = alias.userID
		}
	}
	w.mu.Unlock()
	out := make(map[userBucketKey]userBucketValue, len(deltas))
	for _, delta := range deltas {
		userID := ids[normalizeUsername(delta.Name)]
		if userID == 0 {
			continue
		}
		key := userBucketKey{ServerID: delta.ServerID, InboundID: delta.InboundID, UserID: userID, Bucket: delta.Bucket.UTC()}
		value := out[key]
		value.Upload += delta.Upload
		value.Download += delta.Download
		out[key] = value
	}
	return out, nil
}

func (w *trafficWriter) addUsers(values map[userBucketKey]userBucketValue) {
	if len(values) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for key, value := range values {
		if w.dropped[key.ServerID] {
			continue
		}
		current := w.pendingUsers[key]
		current.Upload += value.Upload
		current.Download += value.Download
		w.pendingUsers[key] = current
	}
}

// restoreUsers puts back a batch whose write failed.
func (w *trafficWriter) restoreUsers(values map[userBucketKey]userBucketValue) {
	w.addUsers(values)
}

// dropUser discards buffered rows for a deleted user. Rows resolved before the
// delete but flushed after it are filtered out by writeUserTrafficBuckets.
func (w *trafficWriter) dropUser(userID uint) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for key := range w.pendingUsers {
		if key.UserID == userID {
			delete(w.pendingUsers, key)
		}
	}
	for name, alias := range w.aliases {
		if alias.userID == userID {
			delete(w.aliases, name)
		}
	}
}

func userTrafficUpsertSet(db *gorm.DB) clause.Set {
	mysql := db.Dialector.Name() == "mysql"
	add := func(column string) clause.Assignment {
		expr := fmt.Sprintf("traffic_user_hourly.%s + excluded.%s", column, column)
		if mysql {
			expr = fmt.Sprintf("%s + VALUES(%s)", column, column)
		}
		return clause.Assignment{Column: clause.Column{Name: column}, Value: gorm.Expr(expr)}
	}
	latest := clause.Assignment{Column: clause.Column{Name: "updated_at"}, Value: gorm.Expr("excluded.updated_at")}
	if mysql {
		latest.Value = gorm.Expr("VALUES(updated_at)")
	}
	return clause.Set{add("upload"), add("download"), latest}
}

var userTrafficConflictColumns = []clause.Column{{Name: "server_id"}, {Name: "inbound_id"}, {Name: "user_id"}, {Name: "bucket"}}

// writeUserTrafficBuckets upserts per-user hourly rows, skipping users that no
// longer exist so a delete racing a flush cannot leave orphan history.
func writeUserTrafficBuckets(db *gorm.DB, values map[userBucketKey]userBucketValue) error {
	if len(values) == 0 {
		return nil
	}
	userIDs := make([]uint, 0, len(values))
	seen := map[uint]bool{}
	for key := range values {
		if !seen[key.UserID] {
			seen[key.UserID] = true
			userIDs = append(userIDs, key.UserID)
		}
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var existing []uint
		if err := tx.Model(&model.User{}).Where("id IN ?", userIDs).Pluck("id", &existing).Error; err != nil {
			return err
		}
		alive := make(map[uint]bool, len(existing))
		for _, id := range existing {
			alive[id] = true
		}
		rows := make([]model.TrafficUserHourly, 0, len(values))
		for key, value := range values {
			if !alive[key.UserID] || (value.Upload == 0 && value.Download == 0) {
				continue
			}
			rows = append(rows, model.TrafficUserHourly{
				ServerID: key.ServerID, InboundID: key.InboundID, UserID: key.UserID, Bucket: key.Bucket,
				Upload: value.Upload, Download: value.Download,
			})
		}
		if len(rows) == 0 {
			return nil
		}
		sort.Slice(rows, func(i, j int) bool {
			a, b := rows[i], rows[j]
			if a.ServerID != b.ServerID {
				return a.ServerID < b.ServerID
			}
			if a.InboundID != b.InboundID {
				return a.InboundID < b.InboundID
			}
			if a.UserID != b.UserID {
				return a.UserID < b.UserID
			}
			return a.Bucket.Before(b.Bucket)
		})
		return tx.Clauses(clause.OnConflict{Columns: userTrafficConflictColumns, DoUpdates: userTrafficUpsertSet(tx)}).
			CreateInBatches(&rows, 200).Error
	})
}

// ---------- admin read path ----------

type userTrafficInbound struct {
	ServerID   uint   `json:"server_id"`
	ServerName string `json:"server_name"`
	InboundID  uint   `json:"inbound_id"`
	Tag        string `json:"tag"`
	Port       int    `json:"port"`
	Type       string `json:"type"`
	// Deleted marks history of an inbound or server removed since.
	Deleted  bool   `json:"deleted"`
	Upload   uint64 `json:"upload"`
	Download uint64 `json:"download"`
}

type userTrafficSeries struct {
	Range       string               `json:"range"`
	StepSeconds int64                `json:"step_seconds"`
	Timezone    string               `json:"timezone"`
	Upload      uint64               `json:"upload"`
	Download    uint64               `json:"download"`
	Points      []trafficPoint       `json:"points"`
	Inbounds    []userTrafficInbound `json:"inbounds"`
}

type userTrafficRow struct {
	ServerID    uint  `gorm:"column:server_id"`
	InboundID   uint  `gorm:"column:inbound_id"`
	BucketIndex int64 `gorm:"column:bucket_index"`
	Upload      uint64
	Download    uint64
}

// userTraffic returns one user's attributed traffic (admin only). Per-user
// history is hourly, so only the 24h/7d/30d ranges are offered.
func (a *App) userTraffic(c *gin.Context) {
	userID, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var user model.User
	if err := a.db.Select("id").First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	rangeName, rangeConfig, ok := a.parseTrafficRange(c)
	if !ok {
		return
	}
	if !rangeConfig.hourly {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unsupported range %q", rangeName)})
		return
	}
	a.flushTraffic()
	loc := a.location()
	start, end := trafficWindow(time.Now(), rangeConfig, loc)
	step := rangeConfig.step
	stepSeconds := int64(step / time.Second)
	offset := stepOffsetSeconds(step, start)
	bucketExpression, err := trafficBucketExpression(a.db, stepSeconds, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var rows []userTrafficRow
	if err := a.db.Model(&model.TrafficUserHourly{}).
		Select("server_id, inbound_id, "+bucketExpression+" AS bucket_index, SUM(upload) AS upload, SUM(download) AS download").
		Where("user_id = ? AND bucket >= ? AND bucket < ?", userID, start, end).
		Group("server_id, inbound_id, " + bucketExpression).
		Order("bucket_index, server_id, inbound_id").
		Limit(maxTrafficQueryRows + 1).
		Scan(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(rows) > maxTrafficQueryRows {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "traffic query produced too many aggregated rows"})
		return
	}
	points := emptyTrafficPoints(start, end, step)
	type inboundKey struct{ serverID, inboundID uint }
	totals := map[inboundKey]*userTrafficInbound{}
	for _, row := range rows {
		bucket := row.BucketIndex*stepSeconds - offset
		if index := trafficPointIndex(bucket, start, end, step, len(points)); index >= 0 {
			points[index].Upload += row.Upload
			points[index].Download += row.Download
		}
		key := inboundKey{row.ServerID, row.InboundID}
		entry := totals[key]
		if entry == nil {
			entry = &userTrafficInbound{ServerID: row.ServerID, InboundID: row.InboundID}
			totals[key] = entry
		}
		entry.Upload += row.Upload
		entry.Download += row.Download
	}
	inbounds := make([]userTrafficInbound, 0, len(totals))
	if len(totals) > 0 {
		inboundIDs := make([]uint, 0, len(totals))
		serverIDs := make([]uint, 0, len(totals))
		for key := range totals {
			inboundIDs = append(inboundIDs, key.inboundID)
			serverIDs = append(serverIDs, key.serverID)
		}
		var inboundRows []model.Inbound
		if err := a.db.Select("id", "server_id", "tag", "type", "listen_port").Where("id IN ?", inboundIDs).Find(&inboundRows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		var serverRows []model.Server
		if err := a.db.Select("id", "name").Where("id IN ?", serverIDs).Find(&serverRows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		inboundByID := make(map[uint]model.Inbound, len(inboundRows))
		for _, inbound := range inboundRows {
			inboundByID[inbound.ID] = inbound
		}
		serverNames := make(map[uint]string, len(serverRows))
		for _, server := range serverRows {
			serverNames[server.ID] = server.Name
		}
		for _, entry := range totals {
			inbound, found := inboundByID[entry.InboundID]
			serverName, serverFound := serverNames[entry.ServerID]
			entry.ServerName = serverName
			entry.Deleted = !found || !serverFound || inbound.ServerID != entry.ServerID
			if found {
				entry.Tag, entry.Type, entry.Port = inbound.Tag, string(inbound.Type), inbound.ListenPort
			}
			inbounds = append(inbounds, *entry)
		}
	}
	sort.Slice(inbounds, func(i, j int) bool {
		return inbounds[i].Upload+inbounds[i].Download > inbounds[j].Upload+inbounds[j].Download
	})
	upload, download := sumTraffic(points)
	c.JSON(http.StatusOK, userTrafficSeries{
		Range: rangeName, StepSeconds: stepSeconds, Timezone: loc.String(),
		Upload: upload, Download: download, Points: points, Inbounds: inbounds,
	})
}

// userTrafficTotalsSince sums attributed traffic per user since a moment.
func userTrafficTotalsSince(db *gorm.DB, since time.Time) (map[uint]userBucketValue, error) {
	var rows []struct {
		UserID   uint `gorm:"column:user_id"`
		Upload   uint64
		Download uint64
	}
	if err := db.Model(&model.TrafficUserHourly{}).
		Select("user_id, SUM(upload) AS upload, SUM(download) AS download").
		Where("bucket >= ?", since.UTC().Truncate(time.Hour)).
		Group("user_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]userBucketValue, len(rows))
	for _, row := range rows {
		out[row.UserID] = userBucketValue{Upload: row.Upload, Download: row.Download}
	}
	return out, nil
}
