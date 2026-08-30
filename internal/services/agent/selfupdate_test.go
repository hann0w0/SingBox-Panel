package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAgentChecksum(t *testing.T) {
	sum := sha256.Sum256([]byte("agent"))
	want := hex.EncodeToString(sum[:])
	got, err := parseAgentChecksum("  " + strings.ToUpper(want) + "\n")
	if err != nil || got != want {
		t.Fatalf("checksum = %q, err=%v; want %q", got, err, want)
	}
	for _, bad := range []string{"", "abc", strings.Repeat("z", 64), strings.Repeat("0", 62)} {
		if _, err := parseAgentChecksum(bad); err == nil {
			t.Fatalf("invalid checksum %q accepted", bad)
		}
	}
}

func TestValidateAgentBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'singbox-panel-agent test-build\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	version, err := validateAgentBinary(context.Background(), path)
	if err != nil || version != "test-build" {
		t.Fatalf("version = %q, err=%v", version, err)
	}
}

func TestUpgradeWatchdogKeepsSingboxUntouched(t *testing.T) {
	for _, required := range []string{agentPreviousBinary, agentReadyFile, "systemctl restart singbox-panel-agent.service"} {
		if !strings.Contains(agentUpgradeScriptBody, required) {
			t.Fatalf("upgrade watchdog missing %q", required)
		}
	}
	if strings.Contains(agentUpgradeScriptBody, "/etc/sing-box") ||
		strings.Contains(agentUpgradeScriptBody, "sing-box.service") {
		t.Fatal("Agent upgrade watchdog must not modify sing-box")
	}
	if strings.Contains(agentUpgradeScriptBody, `rm -f "$BIN"`) {
		t.Fatal("rollback deletes the live Agent before the previous binary is ready")
	}
	for _, required := range []string{"rollback-ok", "rollback-failed"} {
		if !strings.Contains(agentUpgradeScriptBody, required) {
			t.Fatalf("upgrade watchdog missing status %q", required)
		}
	}
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(agentUpgradeScriptBody)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("upgrade watchdog shell syntax: %v: %s", err, out)
	}
}

func TestSelfUpdateAuthenticatesDownload(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	seenAuthorization := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuthorization = r.Header.Get("Authorization")
		http.Error(w, "stop before filesystem staging", http.StatusUnauthorized)
	}))
	defer server.Close()
	if _, err := SelfUpdate(context.Background(), server.URL, token, false); err == nil {
		t.Fatal("SelfUpdate unexpectedly succeeded")
	}
	if seenAuthorization != "Bearer "+token {
		t.Fatalf("Authorization = %q", seenAuthorization)
	}
}

func TestRestoreFileFromPreviousReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "agent")
	previous := filepath.Join(dir, "agent.prev")
	if err := os.WriteFile(live, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previous, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := restoreFileFromPrevious(previous, live); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "old" {
		t.Fatalf("live binary = %q, want old", raw)
	}
}

func TestRestoreFileFromPreviousKeepsLiveWhenBackupMissing(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "agent")
	if err := os.WriteFile(live, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := restoreFileFromPrevious(filepath.Join(dir, "missing.prev"), live); err == nil {
		t.Fatal("missing previous binary must fail")
	}
	raw, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "new" {
		t.Fatalf("live binary changed after failed rollback: %q", raw)
	}
}
