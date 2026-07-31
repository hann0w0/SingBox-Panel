package panel

import (
	"testing"
	"time"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/protocol"
)

func TestRecordServerTrafficSurvivesCounterReset(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "traffic", AgentToken: "traffic-token"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	samples := []*protocol.TrafficSnapshot{
		{UploadTotal: 100, DownloadTotal: 200, SampledAt: now.Unix()},
		{UploadTotal: 150, DownloadTotal: 260, UploadRate: 5, DownloadRate: 6, SampledAt: now.Add(time.Minute).Unix()},
		// A lower cumulative value means sing-box restarted; the new process's
		// current total is added instead of subtracting unsigned counters.
		{UploadTotal: 10, DownloadTotal: 20, UploadRate: 1, DownloadRate: 2, SampledAt: now.Add(2 * time.Minute).Unix()},
	}
	for _, sample := range samples {
		if err := recordServerTraffic(db, server.ID, sample); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.First(&server, server.ID).Error; err != nil {
		t.Fatal(err)
	}
	if server.TrafficUpload != 160 || server.TrafficDownload != 280 {
		t.Fatalf("unexpected totals: upload=%d download=%d", server.TrafficUpload, server.TrafficDownload)
	}
	if server.TrafficUploadRate != 1 || server.TrafficDownloadRate != 2 || !server.TrafficAvailable {
		t.Fatalf("unexpected live state: %+v", server)
	}
	var records []model.TrafficRecord
	if err := db.Where("server_id = ?", server.ID).Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	var upload, download uint64
	for _, record := range records {
		upload += record.Upload
		download += record.Download
	}
	if upload != 160 || download != 280 {
		t.Fatalf("unexpected history totals: upload=%d download=%d", upload, download)
	}

	if err := recordServerTraffic(db, server.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&server, server.ID).Error; err != nil {
		t.Fatal(err)
	}
	if server.TrafficAvailable || server.TrafficUpload != 160 || server.TrafficDownload != 280 {
		t.Fatalf("unavailable sample should preserve totals: %+v", server)
	}
}
