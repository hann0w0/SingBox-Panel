package panel

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func validateTrafficReport(report *protocol.TrafficReport, now time.Time) error {
	if report == nil {
		return nil
	}
	if _, err := uuid.Parse(report.Session); err != nil || len(report.Session) != 36 || report.Sequence == 0 {
		return fmt.Errorf("invalid traffic report identity")
	}
	if len(report.Buckets) == 0 || len(report.Buckets) > 128 {
		return fmt.Errorf("invalid traffic batch size")
	}
	for _, bucket := range report.Buckets {
		// Old samples are expected after an outage. Never clamp them to receive
		// time: doing so would move pre-reset usage into the new billing cycle.
		if bucket.Time <= 0 || bucket.Time%60 != 0 || bucket.Time > now.Add(maxAgentClockSkew).Unix() {
			return fmt.Errorf("invalid traffic bucket timestamp")
		}
	}
	return nil
}

// recordTimestampedTraffic persists history and its receipt in the same
// transaction. Unlike legacy cumulative telemetry, version 2 heartbeats
// cannot contribute bytes, and duplicate batches cannot be charged twice.
func recordTimestampedTraffic(db *gorm.DB, serverID uint, snapshot *protocol.TrafficSnapshot, now time.Time) error {
	if err := validateTrafficReport(snapshot.Report, now); err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var server model.Server
		if err := tx.Select("id", "config_mode").First(&server, serverID).Error; err != nil {
			return err
		}
		live := map[string]any{
			"traffic_available":   !snapshot.Unavailable,
			"traffic_upload_rate": snapshot.UploadRate, "traffic_download_rate": snapshot.DownloadRate,
			"traffic_tcp_connections": snapshot.TCPConnections, "traffic_udp_connections": snapshot.UDPConnections,
		}
		if !snapshot.Unavailable {
			live["traffic_updated_at"] = trafficSampleTime(snapshot.SampledAt, now)
			live["traffic_remote_upload"], live["traffic_remote_download"] = snapshot.UploadTotal, snapshot.DownloadTotal
		} else {
			live["traffic_upload_rate"], live["traffic_download_rate"] = 0, 0
			live["traffic_tcp_connections"], live["traffic_udp_connections"] = 0, 0
		}
		if err := tx.Model(&model.Server{}).Where("id = ?", serverID).Updates(live).Error; err != nil {
			return err
		}
		report := snapshot.Report
		if report == nil {
			return nil
		}
		cursor := model.TrafficReportCursor{ServerID: serverID, Session: report.Session}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&cursor).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cursor, "server_id = ? AND session = ?", serverID, report.Session).Error; err != nil {
			return err
		}
		if report.Sequence <= cursor.Sequence {
			return nil
		}
		// Restoring an older panel database can rewind this cursor while the
		// Agent keeps running with newer, already acknowledged sequence numbers.
		// Missing historical batches cannot be recovered from that Agent, but
		// they must not prevent its subsequent usage from being accepted.
		inbounds, err := loadTrafficInbounds(tx, serverID)
		if err != nil {
			return err
		}
		inboundIDs := make(map[string]uint, len(inbounds))
		for _, inbound := range inbounds {
			inboundIDs[inbound.Tag] = inbound.ID
		}
		var deltas []trafficBucketDelta
		var users []userTrafficDelta
		var upload, download uint64
		for _, bucket := range report.Buckets {
			at := time.Unix(bucket.Time, 0).UTC()
			upload += bucket.Upload
			download += bucket.Download
			deltas = append(deltas, trafficBucketDelta{
				Key: trafficBucketKey{ServerID: serverID, Bucket: at},
				Value: trafficBucketValue{Upload: bucket.Upload, Download: bucket.Download,
					UploadRate: bucket.UploadRate, DownloadRate: bucket.DownloadRate,
					TCPConnections: bucket.TCPConnections, UDPConnections: bucket.UDPConnections},
			})
			for _, port := range bucket.Ports {
				if id := inboundIDs[port.Inbound]; id != 0 {
					deltas = append(deltas, trafficBucketDelta{
						Key: trafficBucketKey{ServerID: serverID, InboundID: id, Bucket: at},
						Value: trafficBucketValue{Upload: port.Upload, Download: port.Download,
							UploadRate: port.UploadRate, DownloadRate: port.DownloadRate},
					})
				}
			}
			if server.ConfigMode != model.ConfigModeRaw {
				users = append(users, multiUserTrafficDeltas(serverID, inbounds, bucket.Users, at.Truncate(time.Hour))...)
			}
		}
		resolver := &trafficWriter{db: tx}
		resolved, err := resolver.resolveUserDeltas(users, now)
		if err != nil {
			return err
		}
		if err := writeTrafficBuckets(tx, deltas); err != nil {
			return err
		}
		if err := writeUserTrafficBuckets(tx, resolved); err != nil {
			return err
		}
		if err := tx.Model(&model.Server{}).Where("id = ?", serverID).Updates(map[string]any{
			"traffic_upload":   gorm.Expr("traffic_upload + ?", upload),
			"traffic_download": gorm.Expr("traffic_download + ?", download),
		}).Error; err != nil {
			return err
		}
		return tx.Model(&cursor).Update("sequence", report.Sequence).Error
	})
}
