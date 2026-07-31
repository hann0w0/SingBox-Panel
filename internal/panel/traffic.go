package panel

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/protocol"
)

const (
	trafficRetentionDays = 31
	trafficStorageBucket = 5 * time.Minute
)

type trafficRangeConfig struct {
	duration time.Duration
	step     time.Duration
}

var trafficRanges = map[string]trafficRangeConfig{
	"1h":  {duration: time.Hour, step: 5 * time.Minute},
	"12h": {duration: 12 * time.Hour, step: 30 * time.Minute},
	"24h": {duration: 24 * time.Hour, step: time.Hour},
	"7d":  {duration: 7 * 24 * time.Hour, step: 6 * time.Hour},
	"30d": {duration: 30 * 24 * time.Hour, step: 24 * time.Hour},
}

func trafficDelta(current, previous uint64) uint64 {
	if current >= previous {
		return current - previous
	}
	// sing-box restarted and its process-local counter reset.
	return current
}

func recordServerTraffic(db *gorm.DB, serverID uint, snapshot *protocol.TrafficSnapshot) error {
	if snapshot == nil {
		return db.Model(&model.Server{}).Where("id = ?", serverID).Updates(map[string]any{
			"traffic_available":     false,
			"traffic_upload_rate":   0,
			"traffic_download_rate": 0,
		}).Error
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var server model.Server
		if err := tx.Select(
			"id", "traffic_upload", "traffic_download",
			"traffic_remote_upload", "traffic_remote_download",
		).First(&server, serverID).Error; err != nil {
			return err
		}
		uploadDelta := trafficDelta(snapshot.UploadTotal, server.TrafficRemoteUpload)
		downloadDelta := trafficDelta(snapshot.DownloadTotal, server.TrafficRemoteDownload)
		sampledAt := time.Unix(snapshot.SampledAt, 0).UTC()
		if snapshot.SampledAt <= 0 || sampledAt.After(time.Now().UTC().Add(time.Minute)) {
			sampledAt = time.Now().UTC()
		}
		updates := map[string]any{
			"traffic_available":       true,
			"traffic_upload":          server.TrafficUpload + uploadDelta,
			"traffic_download":        server.TrafficDownload + downloadDelta,
			"traffic_upload_rate":     snapshot.UploadRate,
			"traffic_download_rate":   snapshot.DownloadRate,
			"traffic_updated_at":      sampledAt,
			"traffic_remote_upload":   snapshot.UploadTotal,
			"traffic_remote_download": snapshot.DownloadTotal,
		}
		if err := tx.Model(&model.Server{}).Where("id = ?", serverID).Updates(updates).Error; err != nil {
			return err
		}
		if uploadDelta == 0 && downloadDelta == 0 {
			return nil
		}
		bucket := sampledAt.Truncate(trafficStorageBucket)
		record := model.TrafficRecord{
			ServerID: serverID,
			Bucket:   bucket,
			Upload:   uploadDelta,
			Download: downloadDelta,
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "server_id"}, {Name: "inbound_id"}, {Name: "user_id"}, {Name: "bucket"},
			},
			DoUpdates: clause.Assignments(map[string]any{
				"upload":     gorm.Expr("upload + ?", uploadDelta),
				"download":   gorm.Expr("download + ?", downloadDelta),
				"updated_at": time.Now(),
			}),
		}).Create(&record).Error
	})
}

type trafficPoint struct {
	Time     time.Time `json:"time"`
	Upload   uint64    `json:"upload"`
	Download uint64    `json:"download"`
}

type trafficSeries struct {
	Available   bool           `json:"available"`
	Range       string         `json:"range"`
	StepSeconds int64          `json:"step_seconds"`
	UpdatedAt   *time.Time     `json:"updated_at"`
	Points      []trafficPoint `json:"points"`
}

func trafficWindow(now time.Time, config trafficRangeConfig) (start, end time.Time) {
	end = now.UTC().Truncate(config.step).Add(config.step)
	start = end.Add(-config.duration)
	return start, end
}

func buildTrafficPoints(records []model.TrafficRecord, start, end time.Time, step time.Duration) []trafficPoint {
	count := int(end.Sub(start) / step)
	points := make([]trafficPoint, count)
	for i := range points {
		points[i].Time = start.Add(time.Duration(i) * step)
	}
	for _, record := range records {
		bucket := record.Bucket.UTC()
		if bucket.Before(start) || !bucket.Before(end) {
			continue
		}
		index := int(bucket.Sub(start) / step)
		if index < 0 || index >= len(points) {
			continue
		}
		points[index].Upload += record.Upload
		points[index].Download += record.Download
	}
	return points
}

func (a *App) serverTraffic(c *gin.Context) {
	serverID, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var server model.Server
	if err := a.db.Select(
		"id", "traffic_available", "traffic_updated_at",
	).First(&server, serverID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	rangeName := c.DefaultQuery("range", "24h")
	rangeConfig, exists := trafficRanges[rangeName]
	if !exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unsupported range %q; use 1h, 12h, 24h, 7d or 30d", rangeName)})
		return
	}
	start, end := trafficWindow(time.Now(), rangeConfig)
	var records []model.TrafficRecord
	if err := a.db.Where(
		"server_id = ? AND inbound_id = 0 AND user_id = 0 AND bucket >= ? AND bucket < ?",
		serverID, start, end,
	).Order("bucket").Find(&records).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, trafficSeries{
		Available:   server.TrafficAvailable,
		Range:       rangeName,
		StepSeconds: int64(rangeConfig.step / time.Second),
		UpdatedAt:   server.TrafficUpdatedAt,
		Points:      buildTrafficPoints(records, start, end, rangeConfig.step),
	})
}

func pruneTrafficRecords(db *gorm.DB, now time.Time) error {
	cutoff := now.UTC().AddDate(0, 0, -trafficRetentionDays).Truncate(24 * time.Hour)
	return db.Where("bucket < ?", cutoff).Delete(&model.TrafficRecord{}).Error
}
