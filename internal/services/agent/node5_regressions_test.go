package agent

import (
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func TestTrafficSamplerCalibratesClockOffsetFromServerTime(t *testing.T) {
	s := newTrafficSampler()
	serverNow := time.Date(2026, 11, 25, 10, 0, 20, 0, time.UTC)
	fastNow := serverNow.Add(2 * time.Hour)
	s.nowFunc = func() time.Time { return fastNow }

	s.ingest("api", clashTrafficResponse{}, fastNow.Add(-5*time.Second))
	s.ingest("api", clashTrafficResponse{UploadTotal: 500, DownloadTotal: 800}, fastNow)

	snap := s.accountingSnapshot()
	if snap == nil || snap.Report == nil || snap.Report.Sequence != 1 {
		t.Fatalf("unexpected initial snapshot: %+v", snap)
	}
	if snap.Report.Buckets[0].Time != fastNow.Truncate(time.Minute).Unix() {
		t.Fatalf("initial bucket time = %d, want fast time %d", snap.Report.Buckets[0].Time, fastNow.Truncate(time.Minute).Unix())
	}

	// Panel rejects future bucket with Sequence=0 and delivers its ServerTime.
	s.acknowledgeReport(protocol.TrafficAck{
		Session:    snap.Report.Session,
		Sequence:   0,
		ServerTime: serverNow.Unix(),
	})

	if s.report == nil || s.report.Sequence != 1 {
		t.Fatalf("sequence-0 clock-sync ack prematurely dropped report: %+v", s.report)
	}
	if len(s.report.Buckets) != 1 || s.report.Buckets[0].Time != serverNow.Truncate(time.Minute).Unix() {
		t.Fatalf("report bucket not rebased to server time %d: %+v", serverNow.Truncate(time.Minute).Unix(), s.report.Buckets)
	}

	// Retrying the same report with valid ack now succeeds and drains report.
	retry := s.accountingSnapshot()
	if retry.Report.Sequence != 1 || retry.Report.Buckets[0].Time != serverNow.Truncate(time.Minute).Unix() {
		t.Fatalf("retried report mismatch: %+v", retry.Report)
	}
	s.acknowledgeReport(protocol.TrafficAck{
		Session:    retry.Report.Session,
		Sequence:   1,
		ServerTime: serverNow.Unix(),
	})
	if s.report != nil {
		t.Fatalf("valid ack did not clear report: %+v", s.report)
	}

	// Next ingest uses calibrated clockOffset (~ -2h) for new buckets.
	s.ingest("api", clashTrafficResponse{UploadTotal: 750, DownloadTotal: 1000}, fastNow.Add(5*time.Second))
	nextSnap := s.accountingSnapshot()
	if nextSnap.Report == nil || nextSnap.Report.Buckets[0].Time != serverNow.Truncate(time.Minute).Unix() {
		t.Fatalf("post-calibration bucket time = %+v, want calibrated server minute %d", nextSnap.Report, serverNow.Truncate(time.Minute).Unix())
	}
}

func TestTrafficSamplerResetsBothDirectionsOnAsymmetricRestart(t *testing.T) {
	s := newTrafficSampler()
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

	s.ingest("api", clashTrafficResponse{UploadTotal: 1000, DownloadTotal: 5000}, now)
	// Simulate sing-box restart where download restarted below 5000 while new
	// upload already surpassed 1000 before the next sample poll.
	s.ingest("api", clashTrafficResponse{UploadTotal: 1200, DownloadTotal: 300}, now.Add(time.Second))

	s.mu.Lock()
	bucket := s.buckets[now.Truncate(time.Minute).Unix()]
	s.mu.Unlock()
	if bucket == nil || bucket.Upload != 1200 || bucket.Download != 300 {
		t.Fatalf("asymmetric restart delta = %+v, want upload=1200 download=300", bucket)
	}
}

func TestTrafficSpoolRoundTripAndPermissions(t *testing.T) {
	dir := t.TempDir()
	spoolPath := filepath.Join(dir, "state", "traffic-spool.json")

	src := newTrafficSampler()
	src.spoolPath = spoolPath
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	src.ingest("api", clashTrafficResponse{}, now)
	src.ingest("api", clashTrafficResponse{UploadTotal: 900, DownloadTotal: 1100}, now.Add(time.Second))
	snap := src.accountingSnapshot()
	// Also queue a newer bucket while snap.Report is still unacknowledged.
	src.ingest("api", clashTrafficResponse{UploadTotal: 1200, DownloadTotal: 1500}, now.Add(time.Minute))

	if err := src.saveSpool(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(spoolPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("spool file permissions = %o, want 0600", info.Mode().Perm())
	}

	dst := newTrafficSampler()
	dst.spoolPath = spoolPath
	if err := dst.loadSpool(); err != nil {
		t.Fatal(err)
	}
	if dst.report == nil || dst.report.Session != snap.Report.Session || dst.report.Sequence != 1 {
		t.Fatalf("loaded unacked report = %+v, want seq 1 session %q", dst.report, snap.Report.Session)
	}
	if dst.reportSequence != 1 || dst.accountingSession != snap.Report.Session {
		t.Fatalf("loaded session=%q seq=%d", dst.accountingSession, dst.reportSequence)
	}
	if dst.buckets[now.Add(time.Minute).Truncate(time.Minute).Unix()].Upload != 300 {
		t.Fatalf("loaded pending bucket = %+v, want upload=300", dst.buckets)
	}
	if _, err := os.Stat(spoolPath); !os.IsNotExist(err) {
		t.Fatalf("spool file still exists after load: %v", err)
	}
}

func TestOpenRCLogrotateCgroupNoAndShellSyntax(t *testing.T) {
	script := openRCSingboxServiceScript("/usr/bin/sing-box")
	if !strings.Contains(script, `rc_cgroup_cleanup="NO"`) {
		t.Fatalf("OpenRC sing-box service missing rc_cgroup_cleanup=NO:\n%s", script)
	}
	if !strings.Contains(selfUninstallScript, `/proc/$UPGRADE_PID/cmdline`) {
		t.Fatal("self-uninstall script missing /proc cmdline guard before killing upgrade PID")
	}
	for _, s := range []string{script, selfUninstallScript} {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(s)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("shell syntax error: %v: %s", err, out)
		}
	}

	logrotatePath := filepath.Join(t.TempDir(), "logrotate.d", "sing-box")
	if err := ensureOpenRCLogrotateAt(logrotatePath, "/var/log/custom-sing-box.log"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logrotatePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "/var/log/custom-sing-box.log") || !strings.Contains(string(raw), "/var/log/singbox-panel-agent.log") || !strings.Contains(string(raw), "copytruncate") {
		t.Fatalf("unexpected logrotate content:\n%s", raw)
	}
}

func TestPidFileMatchesAtRejectsReusedPID(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "upgrade.pid")
	procRoot := filepath.Join(dir, "proc")
	const pid = 4242
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	procDir := filepath.Join(procRoot, strconv.Itoa(pid))
	if err := os.MkdirAll(procDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procDir, "cmdline"), []byte("/usr/sbin/nginx\x00-g\x00daemon off;\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if pidFileMatchesAt(procRoot, pidFile, "singbox-panel-agent-upgrade", func(int) bool { return true }) {
		t.Fatal("reused PID running nginx was mistakenly accepted")
	}
	if err := os.WriteFile(filepath.Join(procDir, "cmdline"), []byte("/bin/sh\x00/run/singbox-panel-agent-upgrade.sh\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !pidFileMatchesAt(procRoot, pidFile, "singbox-panel-agent-upgrade", func(int) bool { return true }) {
		t.Fatal("matching upgrade process was rejected")
	}
}

func TestHasNoStrayConfigsAtAndWaitForConfigStart(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "config.json"), "{}")
	writeTestFile(t, filepath.Join(dir, "disabled", "extra.json"), "{}")
	if !hasNoStrayConfigsAt(dir) {
		t.Fatal("clean config directory reported stray configs")
	}
	writeTestFile(t, filepath.Join(dir, "stray.json"), "{}")
	if hasNoStrayConfigsAt(dir) {
		t.Fatal("stray.json alongside config.json was ignored")
	}

	busctlOK := systemdServiceManager{run: func(_ context.Context, _ string, _ ...string) (string, error) {
		return "t 1787285641404504", nil
	}}
	if err := busctlOK.waitForConfigStart(context.Background()); err != nil {
		t.Fatalf("busctl waitForConfigStart returned error: %v", err)
	}

	// When busctl fails and config falls inside the current whole second, canceled context aborts wait cleanly.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fixedNow := time.Date(2026, 11, 25, 10, 0, 0, 100*int(time.Millisecond), time.UTC)
	busctlMissing := systemdServiceManager{
		run: func(context.Context, string, ...string) (string, error) {
			return "", os.ErrNotExist
		},
		now: func() time.Time { return fixedNow },
	}
	if err := busctlMissing.waitForConfigStart(ctx); err == nil {
		t.Fatal("expected canceled fallback wait to return context error")
	}
}

func TestRestrictedOutboundHTTPHostAndIPv6Preference(t *testing.T) {
	resolve := func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{
			netip.MustParseAddr("1.1.1.1"),
			netip.MustParseAddr("2606:4700:4700::1111"),
		}, nil
	}

	// Missing transport.host on http transport preserves original domain in []string.
	proxy := restrictedProxy("http.example.com")
	proxy["transport"] = map[string]any{"type": "http", "path": "/h2"}
	prepared, err := prepareRestrictedOutboundsWithPreference(context.Background(), restrictedConfig(t, proxy), []string{"private"}, resolve, true)
	if err != nil {
		t.Fatal(err)
	}
	row := decodeRestrictedOutbounds(t, prepared)[0]
	if string(row["server"]) != `"2606:4700:4700::1111"` {
		t.Fatalf("IPv6 preference picked %s, want 2606:4700:4700::1111", row["server"])
	}
	var tr struct {
		Host []string `json:"host"`
	}
	if err := json.Unmarshal(row["transport"], &tr); err != nil {
		t.Fatal(err)
	}
	if len(tr.Host) != 1 || tr.Host[0] != "http.example.com" {
		t.Fatalf("http transport host = %+v, want [http.example.com]", tr.Host)
	}

	// Existing explicit host is preserved untouched.
	explicit := restrictedProxy("edge.example.com")
	explicit["transport"] = map[string]any{"type": "http", "host": []string{"cdn.custom"}}
	preparedExplicit, err := prepareRestrictedOutbounds(context.Background(), restrictedConfig(t, explicit), []string{"private"}, resolve)
	if err != nil {
		t.Fatal(err)
	}
	rowExplicit := decodeRestrictedOutbounds(t, preparedExplicit)[0]
	if string(rowExplicit["server"]) != `"1.1.1.1"` {
		t.Fatalf("default preference picked %s, want 1.1.1.1", rowExplicit["server"])
	}
	tr.Host = nil
	if err := json.Unmarshal(rowExplicit["transport"], &tr); err != nil {
		t.Fatal(err)
	}
	if len(tr.Host) != 1 || tr.Host[0] != "cdn.custom" {
		t.Fatalf("explicit http host mutated: %+v", tr.Host)
	}
}

func TestObserveConnectionUsesCalibratedClockOffsetAndSpoolBaseline(t *testing.T) {
	s := newTrafficSampler()
	serverNow := time.Date(2026, 11, 25, 11, 57, 0, 0, time.UTC)
	fastLocal := serverNow.Add(5 * time.Minute) // 12:02 local (crosses hour boundary!)
	s.clockOffset = serverNow.Sub(fastLocal)    // -5m

	s.ingest("http://127.0.0.1:9090/connections", clashTrafficResponse{UploadTotal: 1000, DownloadTotal: 2000}, fastLocal.Add(-time.Minute))
	// Observe anonymous bytes on fastLocal (12:02 local -> 11:57 calibrated), then attribute user at +10s.
	s.observeConnection("conn-skew", "vless-in", "", [2]uint64{400, 600}, false, fastLocal)
	s.observeConnection("conn-skew", "vless-in", "u1:token", [2]uint64{500, 800}, false, fastLocal.Add(10*time.Second))

	snap := s.accountingSnapshot()
	if snap == nil || snap.Report == nil || len(snap.Report.Buckets) == 0 {
		t.Fatalf("missing accounting report: %+v", snap)
	}
	maxServerMinute := serverNow.Truncate(time.Minute).Unix()
	for _, b := range snap.Report.Buckets {
		if b.Time > maxServerMinute {
			t.Fatalf("bucket timestamp %d exceeded calibrated server minute %d (uncalibrated local clock leaked)", b.Time, maxServerMinute)
		}
	}

	// Verify L2: spool persists node counter baseline across short Agent restart.
	spoolPath := filepath.Join(t.TempDir(), "traffic-spool.json")
	s.spoolPath = spoolPath
	s.acknowledgeReport(protocol.TrafficAck{
		Session:    snap.Report.Session,
		Sequence:   snap.Report.Sequence,
		ServerTime: serverNow.Unix(),
	})
	if err := s.saveSpool(); err != nil {
		t.Fatal(err)
	}
	restarted := newTrafficSampler()
	restarted.spoolPath = spoolPath
	if err := restarted.loadSpool(); err != nil {
		t.Fatal(err)
	}
	// sing-box stayed running and transferred +350 upload / +700 download while Agent restarted.
	restarted.ingest("http://127.0.0.1:9090/connections", clashTrafficResponse{UploadTotal: 1350, DownloadTotal: 2700}, serverNow.Add(5*time.Second))
	afterRestart := restarted.accountingSnapshot()
	if afterRestart == nil || afterRestart.Report == nil || len(afterRestart.Report.Buckets) != 1 {
		t.Fatalf("missing post-restart delta report: %+v", afterRestart)
	}
	if afterRestart.Report.Buckets[0].Upload != 350 || afterRestart.Report.Buckets[0].Download != 700 {
		t.Fatalf("post-restart delta = %+v, want upload=350 download=700", afterRestart.Report.Buckets[0])
	}
}

func TestMutatingQueueDropsDisconnectedAndExpiredCommands(t *testing.T) {
	c := NewClient("https://panel.example.com", "token", false)
	c.IsMutatingCommand = commandMutates
	c.CommandTimeout = func(protocol.MessageType) time.Duration { return 20 * time.Millisecond }

	var executed []string
	c.OnCommand = func(_ context.Context, env protocol.Envelope) protocol.CommandResultEvt {
		executed = append(executed, env.ID)
		return protocol.CommandResultEvt{ID: env.ID, OK: true}
	}

	rootCtx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	c.startMutatingWorker(rootCtx, &wg)

	c.activeSession.Store(2)
	// 1. Stale session (session 1 != active session 2) must be dropped.
	c.mutatingCommands <- mutatingCommand{
		session:  1,
		deadline: time.Now().Add(time.Minute),
		env:      protocol.Envelope{ID: "stale-session", Type: protocol.CmdServiceAction},
	}
	// 2. Expired deadline on active session must be rejected without calling OnCommand.
	c.mutatingCommands <- mutatingCommand{
		session:  2,
		deadline: time.Now().Add(-time.Millisecond),
		env:      protocol.Envelope{ID: "expired-cmd", Type: protocol.CmdServiceAction},
	}
	// 3. Valid active command must still execute.
	c.mutatingCommands <- mutatingCommand{
		session:  2,
		deadline: time.Now().Add(time.Minute),
		env:      protocol.Envelope{ID: "fresh-cmd", Type: protocol.CmdServiceAction},
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(c.mutatingCommands) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(25 * time.Millisecond)
	cancel()
	wg.Wait()

	if len(executed) != 1 || executed[0] != "fresh-cmd" {
		t.Fatalf("executed commands = %v, want [fresh-cmd]", executed)
	}
}
