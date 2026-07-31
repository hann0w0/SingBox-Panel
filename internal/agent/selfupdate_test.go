package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'singpanel-agent test-build\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	version, err := validateAgentBinary(context.Background(), path)
	if err != nil || version != "test-build" {
		t.Fatalf("version = %q, err=%v", version, err)
	}
}

func TestUpgradeWatchdogKeepsSingboxUntouched(t *testing.T) {
	for _, required := range []string{agentPreviousBinary, agentReadyFile, "systemctl restart singpanel-agent.service"} {
		if !strings.Contains(agentUpgradeScriptBody, required) {
			t.Fatalf("upgrade watchdog missing %q", required)
		}
	}
	if strings.Contains(agentUpgradeScriptBody, "/etc/sing-box") ||
		strings.Contains(agentUpgradeScriptBody, "sing-box.service") {
		t.Fatal("Agent upgrade watchdog must not modify sing-box")
	}
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(agentUpgradeScriptBody)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("upgrade watchdog shell syntax: %v: %s", err, out)
	}
}
