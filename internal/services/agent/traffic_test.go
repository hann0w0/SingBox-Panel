package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
