package agent

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSelfUninstallScriptCleansAgentButKeepsSingbox(t *testing.T) {
	for _, path := range []string{
		"/usr/local/bin/singbox-panel-agent",
		"/usr/local/bin/singbox-panel-agent.prev",
		"/etc/singbox-panel-agent",
		"/var/lib/singbox-panel-agent",
		"/etc/systemd/system/singbox-panel-agent.service",
		"/etc/systemd/system/singbox-panel-agent-upgrade.service",
		"/etc/systemd/system/singbox-panel-agent-uninstall.service",
		"/run/singbox-panel-agent-upgrade.pid",
		"/etc/init.d/singbox-panel-agent",
		"rc-update del singbox-panel-agent default",
	} {
		if !strings.Contains(selfUninstallScript, path) {
			t.Fatalf("cleanup script does not remove %s", path)
		}
	}
	if strings.Contains(selfUninstallScript, "/usr/bin/sing-box") ||
		strings.Contains(selfUninstallScript, "/etc/sing-box") ||
		strings.Contains(selfUninstallScript, "sing-box.service") {
		t.Fatal("Agent-only cleanup must not remove sing-box")
	}
}

func TestSelfUninstallScriptHasValidShellSyntax(t *testing.T) {
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(selfUninstallScript)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("self-uninstall shell syntax: %v: %s", err, out)
	}
}
