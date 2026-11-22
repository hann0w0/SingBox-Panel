package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

// TestAlpineLiveEndToEnd exercises the real Alpine Linux sing-box lifecycle
// inside an Alpine + OpenRC container (see scripts/alpine-smoke.sh). It is
// skipped unless SINGBOX_PANEL_ALPINE_LIVE=1 is explicitly set so normal
// developer and unit test runs never touch host paths.
func TestAlpineLiveEndToEnd(t *testing.T) {
	if os.Getenv("SINGBOX_PANEL_ALPINE_LIVE") != "1" {
		t.Skip("set SINGBOX_PANEL_ALPINE_LIVE=1 inside an Alpine container to run")
	}

	if !isAlpineHost() {
		t.Fatal("expected isAlpineHost() == true on Alpine container")
	}
	manager, err := currentServiceManager()
	if err != nil {
		t.Fatalf("currentServiceManager() on Alpine: %v", err)
	}
	if manager.Kind() != serviceKindOpenRC {
		t.Fatalf("service manager kind = %q, want %q", manager.Kind(), serviceKindOpenRC)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// 1. Install official musl sing-box release via the default "script" method.
	installOut, err := InstallSingbox(ctx, protocol.ChannelStable, "", protocol.MethodScript)
	if err != nil {
		t.Fatalf("InstallSingbox on Alpine failed: %v\noutput:\n%s", err, installOut)
	}
	installed, ver := DetectVersion(ctx)
	if !installed || !versionRe.MatchString(ver) {
		t.Fatalf("DetectVersion after install = (%v, %q)", installed, ver)
	}
	t.Logf("installed sing-box %s on Alpine", ver)

	// 2. Verify OpenRC service definition and config validation on BusyBox ash.
	if !manager.Enabled(ctx) {
		t.Fatal("expected sing-box service to be enabled in OpenRC default runlevel")
	}
	if out, err := run(ctx, "sh", "-n", openRCSingboxInit); err != nil {
		t.Fatalf("BusyBox sh -n on %s failed: %v: %s", openRCSingboxInit, err, out)
	}
	minimalCfg := `{"log":{"level":"info"},"inbounds":[],"outbounds":[{"type":"direct","tag":"direct"}]}`
	if err := os.WriteFile(ConfigFile, []byte(minimalCfg), 0o600); err != nil {
		t.Fatalf("write minimal config: %v", err)
	}
	if out, err := run(ctx, SingboxBinary, "check", "-D", singboxStateDir, "-C", ConfigDir); err != nil {
		t.Fatalf("sing-box check failed: %v: %s", err, out)
	}

	// 3. Verify BusyBox ash syntax of Agent upgrade & self-uninstall scripts.
	for name, body := range map[string]string{
		"agentUpgradeScriptBody": agentUpgradeScriptBody,
		"selfUninstallScript":    selfUninstallScript,
	} {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-n")
		cmd.Stdin = strings.NewReader(body)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("BusyBox sh -n on %s failed: %v: %s", name, err, out)
		}
	}

	// 4. Verify UninstallSingbox removes binary & managed OpenRC init while keeping config.
	uninstallOut, err := UninstallSingbox(ctx)
	if err != nil {
		t.Fatalf("UninstallSingbox on Alpine failed: %v\noutput:\n%s", err, uninstallOut)
	}
	if _, err := os.Stat(SingboxBinary); !os.IsNotExist(err) {
		t.Fatalf("expected %s removed after uninstall, stat err=%v", SingboxBinary, err)
	}
	if _, err := os.Stat(openRCSingboxInit); !os.IsNotExist(err) {
		t.Fatalf("expected managed %s removed after uninstall, stat err=%v", openRCSingboxInit, err)
	}
	preserved, err := os.ReadFile(filepath.Join(ConfigDir, "config.json"))
	if err != nil || string(preserved) != minimalCfg {
		t.Fatalf("expected config.json preserved after uninstall: got %q err=%v", string(preserved), err)
	}
}
