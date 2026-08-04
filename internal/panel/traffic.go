package panel

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/protocol"
)

const (
	trafficRetentionDays = 31
	trafficStorageBucket = time.Minute // 1-minute buckets; display step aggregates as needed
)

type trafficRangeConfig struct {
	duration time.Duration
	step     time.Duration
}

var trafficRanges = map[string]trafficRangeConfig{
	"15m": {duration: 15 * time.Minute, step: time.Minute},
	"30m": {duration: 30 * time.Minute, step: 2 * time.Minute},
	"1h":  {duration: time.Hour, step: 2 * time.Minute},
	"12h": {duration: 12 * time.Hour, step: 30 * time.Minute},
	"24h": {duration: 24 * time.Hour, step: time.Hour},
	"7d":  {duration: 7 * 24 * time.Hour, step: 6 * time.Hour},
	"30d": {duration: 30 * 24 * time.Hour, step: 24 * time.Hour},
}

func trafficDelta(current, previous uint64) uint64 {
	if current >= previous {
		return current - previous
	}
	return current
}

func recordTrafficBucket(tx *gorm.DB, serverID, inboundID uint, bucket time.Time, upload, download, uploadRate, downloadRate uint64, tcpConnections, udpConnections int) error {
	if upload == 0 && download == 0 && uploadRate == 0 && downloadRate == 0 && tcpConnections == 0 && udpConnections == 0 {
		return nil
	}
	record := model.TrafficRecord{
		ServerID: serverID, InboundID: inboundID, Bucket: bucket,
		Upload: upload, Download: download, UploadRate: uploadRate, DownloadRate: downloadRate,
		TCPConnections: tcpConnections, UDPConnections: udpConnections,
	}
	var existing model.TrafficRecord
	err := tx.Where("server_id = ? AND inbound_id = ? AND bucket = ?", serverID, inboundID, bucket).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&record).Error
	}
	if err != nil {
		return err
	}
	existing.Upload += upload
	existing.Download += download
	if uploadRate > existing.UploadRate {
		existing.UploadRate = uploadRate
	}
	if downloadRate > existing.DownloadRate {
		existing.DownloadRate = downloadRate
	}
	existing.TCPConnections = tcpConnections
	existing.UDPConnections = udpConnections
	existing.UpdatedAt = time.Now()
	return tx.Save(&existing).Error
}

func recordServerTraffic(db *gorm.DB, serverID uint, snapshot *protocol.TrafficSnapshot) error {
	if snapshot == nil {
		return db.Model(&model.Server{}).Where("id = ?", serverID).Updates(map[string]any{
			"traffic_available":       false,
			"traffic_upload_rate":     0,
			"traffic_download_rate":   0,
			"traffic_tcp_connections": 0,
			"traffic_udp_connections": 0,
		}).Error
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var server model.Server
		if err := tx.Select(
			"id", "traffic_available", "traffic_upload", "traffic_download",
			"traffic_remote_upload", "traffic_remote_download",
		).First(&server, serverID).Error; err != nil {
			return err
		}

		// The first successful sample establishes a baseline. This prevents old
		// traffic from before the feature was enabled being counted as new usage.
		uploadDelta := uint64(0)
		downloadDelta := uint64(0)
		if server.TrafficAvailable {
			uploadDelta = trafficDelta(snapshot.UploadTotal, server.TrafficRemoteUpload)
			downloadDelta = trafficDelta(snapshot.DownloadTotal, server.TrafficRemoteDownload)
		}
		sampledAt := time.Unix(snapshot.SampledAt, 0).UTC()
		if snapshot.SampledAt <= 0 || sampledAt.After(time.Now().UTC().Add(time.Minute)) {
			sampledAt = time.Now().UTC()
		}
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
		if err := recordTrafficBucket(tx, serverID, 0, bucket, uploadDelta, downloadDelta, snapshot.UploadRate, snapshot.DownloadRate, snapshot.TCPConnections, snapshot.UDPConnections); err != nil {
			return err
		}

		if len(snapshot.Ports) == 0 {
			return nil
		}
		var inbounds []model.Inbound
		if err := tx.Where("server_id = ?", serverID).Find(&inbounds).Error; err != nil {
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
			if err := recordTrafficBucket(tx, serverID, inboundID, bucket, port.Upload, port.Download, port.UploadRate, port.DownloadRate, 0, 0); err != nil {
				return err
			}
		}
		return nil
	})
}

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
	Available      bool                `json:"available"`
	Range          string              `json:"range"`
	StepSeconds    int64               `json:"step_seconds"`
	UpdatedAt      *time.Time          `json:"updated_at"`
	Upload         uint64              `json:"upload"`
	Download       uint64              `json:"download"`
	UploadRate     uint64              `json:"upload_rate"`
	DownloadRate   uint64              `json:"download_rate"`
	TCPConnections int                 `json:"tcp_connections"`
	UDPConnections int                 `json:"udp_connections"`
	Points         []trafficPoint      `json:"points"`
	Ports          []trafficPortSeries `json:"ports"`
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
		if record.UploadRate > points[index].UploadRate {
			points[index].UploadRate = record.UploadRate
		}
		if record.DownloadRate > points[index].DownloadRate {
			points[index].DownloadRate = record.DownloadRate
		}
		points[index].TCPConnections = record.TCPConnections
		points[index].UDPConnections = record.UDPConnections
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

	rangeName := c.DefaultQuery("range", "24h")
	rangeConfig, exists := trafficRanges[rangeName]
	if !exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unsupported range %q", rangeName)})
		return
	}
	start, end := trafficWindow(time.Now(), rangeConfig)
	var records []model.TrafficRecord
	if err := a.db.Where(
		"server_id = ? AND bucket >= ? AND bucket < ?", serverID, start, end,
	).Order("bucket").Find(&records).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	totalRecords := make([]model.TrafficRecord, 0)
	byInbound := make(map[uint][]model.TrafficRecord)
	for _, record := range records {
		if record.InboundID == 0 {
			totalRecords = append(totalRecords, record)
		} else {
			byInbound[record.InboundID] = append(byInbound[record.InboundID], record)
		}
	}
	totalPoints := buildTrafficPoints(totalRecords, start, end, rangeConfig.step)
	totalUpload, totalDownload := sumTraffic(totalPoints)

	var inbounds []model.Inbound
	if err := a.db.Where("server_id = ?", serverID).Order("listen_port, id").Find(&inbounds).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ports := make([]trafficPortSeries, 0, len(inbounds))
	for _, inbound := range inbounds {
		points := buildTrafficPoints(byInbound[inbound.ID], start, end, rangeConfig.step)
		upload, download := sumTraffic(points)
		ports = append(ports, trafficPortSeries{
			InboundID: inbound.ID, Tag: inbound.Tag, Port: inbound.ListenPort, Type: string(inbound.Type),
			Upload: upload, Download: download, Points: points,
		})
	}

	c.JSON(http.StatusOK, trafficSeries{
		Available: server.TrafficAvailable,
		Range:     rangeName, StepSeconds: int64(rangeConfig.step / time.Second), UpdatedAt: server.TrafficUpdatedAt,
		Upload: totalUpload, Download: totalDownload,
		UploadRate: server.TrafficUploadRate, DownloadRate: server.TrafficDownloadRate,
		TCPConnections: server.TrafficTCPConnections, UDPConnections: server.TrafficUDPConnections,
		Points: totalPoints, Ports: ports,
	})
}

func pruneTrafficRecords(db *gorm.DB, now time.Time) error {
	cutoff := now.UTC().AddDate(0, 0, -trafficRetentionDays).Truncate(24 * time.Hour)
	return db.Where("bucket < ?", cutoff).Delete(&model.TrafficRecord{}).Error
}
