package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func TestCountProcSocketsAcceptsSequenceColon(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tcp")
	contents := strings.Join([]string{
		"  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode",
		"   0: 0100007F:1F90 0200007F:C350 01 00000000:00000000 00:00000000 00000000 1000 0 12345 1",
		"   1: 00000000:1F91 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 12346 1",
	}, "\n")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := countProcSockets(path, false); got != 2 {
		t.Fatalf("all sockets = %d, want 2", got)
	}
	if got := countProcSockets(path, true); got != 1 {
		t.Fatalf("non-listening sockets = %d, want 1", got)
	}
}

func TestCappedCommandOutputStopsGrowing(t *testing.T) {
	var output cappedCommandOutput
	payload := []byte(strings.Repeat("x", maxCommandOutputBytes+1024))
	n, err := output.Write(payload)
	if err != nil || n != len(payload) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	got := output.String()
	if !strings.HasSuffix(got, "[command output truncated]") {
		t.Fatal("truncation marker missing")
	}
	if len(got) > maxCommandOutputBytes+64 {
		t.Fatalf("buffer grew past cap: %d", len(got))
	}
}

func TestTrafficSnapshotRequiresAcknowledgementBeforeDraining(t *testing.T) {
	sampler := newTrafficSampler()
	sampler.available = true
	sampler.haveSample = true
	sampler.lastSample = time.Now()
	sampler.pendingPorts["vless-in"] = protocol.PortTrafficSnapshot{
		Inbound: "vless-in", Upload: 120, Download: 80, UploadRate: 50, DownloadRate: 40,
	}

	first := sampler.snapshot()
	second := sampler.snapshot()
	if len(first.Ports) != 1 || len(second.Ports) != 1 || second.Ports[0].Upload != 120 {
		t.Fatalf("unacknowledged traffic was drained: first=%+v second=%+v", first.Ports, second.Ports)
	}

	sampler.mu.Lock()
	pending := sampler.pendingPorts["vless-in"]
	pending.Upload += 30
	pending.Download += 20
	pending.UploadRate = 75
	sampler.pendingPorts["vless-in"] = pending
	sampler.mu.Unlock()
	sampler.acknowledge(first)

	remaining := sampler.snapshot()
	if len(remaining.Ports) != 1 || remaining.Ports[0].Upload != 30 || remaining.Ports[0].Download != 20 || remaining.Ports[0].UploadRate != 75 {
		t.Fatalf("acknowledgement removed newer traffic: %+v", remaining.Ports)
	}
	sampler.acknowledge(remaining)
	if got := sampler.snapshot(); len(got.Ports) != 0 {
		t.Fatalf("acknowledged traffic still pending: %+v", got.Ports)
	}
}

func TestTrafficSummaryDoesNotCarryPortDeltas(t *testing.T) {
	sampler := newTrafficSampler()
	sampler.available = true
	sampler.haveSample = true
	sampler.lastSample = time.Now()
	sampler.pendingPorts["in"] = protocol.PortTrafficSnapshot{Inbound: "in", Upload: 10}
	if summary := sampler.summarySnapshot(); summary == nil || len(summary.Ports) != 0 {
		t.Fatalf("summary snapshot = %+v", summary)
	}
	if full := sampler.snapshot(); len(full.Ports) != 1 {
		t.Fatal("summary snapshot consumed pending port traffic")
	}
}
