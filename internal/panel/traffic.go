package panel

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/protocol"
)

const trafficRetentionDays = 400

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
		bucket := sampledAt.Truncate(time.Hour)
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
	Date     string `json:"date"`
	Upload   uint64 `json:"upload"`
	Download uint64 `json:"download"`
}

type trafficSummary struct {
	Available     bool           `json:"available"`
	Upload        uint64         `json:"upload"`
	Download      uint64         `json:"download"`
	UploadRate    uint64         `json:"upload_rate"`
	DownloadRate  uint64         `json:"download_rate"`
	TodayUpload   uint64         `json:"today_upload"`
	TodayDownload uint64         `json:"today_download"`
	MonthUpload   uint64         `json:"month_upload"`
	MonthDownload uint64         `json:"month_download"`
	UpdatedAt     *time.Time     `json:"updated_at"`
	History       []trafficPoint `json:"history"`
	RetentionDays int            `json:"retention_days"`
}

func (a *App) serverTraffic(c *gin.Context) {
	serverID, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var server model.Server
	if err := a.db.Select(
		"id", "traffic_available", "traffic_upload", "traffic_download",
		"traffic_upload_rate", "traffic_download_rate", "traffic_updated_at",
	).First(&server, serverID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	days := 30
	if raw := c.Query("days"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > trafficRetentionDays {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("days must be between 1 and %d", trafficRetentionDays)})
			return
		}
		days = value
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	historyStart := today.AddDate(0, 0, -(days - 1))
	queryStart := historyStart
	if month.Before(queryStart) {
		queryStart = month
	}
	var records []model.TrafficRecord
	if err := a.db.Where(
		"server_id = ? AND inbound_id = 0 AND user_id = 0 AND bucket >= ?",
		serverID, queryStart,
	).Order("bucket").Find(&records).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	points := make(map[string]*trafficPoint, days)
	for i := 0; i < days; i++ {
		date := historyStart.AddDate(0, 0, i).Format("2006-01-02")
		points[date] = &trafficPoint{Date: date}
	}
	summary := trafficSummary{
		Available:     server.TrafficAvailable,
		Upload:        server.TrafficUpload,
		Download:      server.TrafficDownload,
		UploadRate:    server.TrafficUploadRate,
		DownloadRate:  server.TrafficDownloadRate,
		UpdatedAt:     server.TrafficUpdatedAt,
		RetentionDays: trafficRetentionDays,
	}
	for _, record := range records {
		if !record.Bucket.Before(today) {
			summary.TodayUpload += record.Upload
			summary.TodayDownload += record.Download
		}
		if !record.Bucket.Before(month) {
			summary.MonthUpload += record.Upload
			summary.MonthDownload += record.Download
		}
		date := record.Bucket.UTC().Format("2006-01-02")
		if point := points[date]; point != nil {
			point.Upload += record.Upload
			point.Download += record.Download
		}
	}
	for i := 0; i < days; i++ {
		date := historyStart.AddDate(0, 0, i).Format("2006-01-02")
		summary.History = append(summary.History, *points[date])
	}
	c.JSON(http.StatusOK, summary)
}

func pruneTrafficRecords(db *gorm.DB, now time.Time) error {
	cutoff := now.UTC().AddDate(0, 0, -trafficRetentionDays).Truncate(24 * time.Hour)
	return db.Where("bucket < ?", cutoff).Delete(&model.TrafficRecord{}).Error
}
