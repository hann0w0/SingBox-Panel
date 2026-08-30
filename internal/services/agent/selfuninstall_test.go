package agent

import (
	"strings"
	"testing"
)

func TestSelfUninstallScriptCleansAgentButKeepsSingbox(t *testing.T) {
	for _, path := range []string{
		"/usr/local/bin/singbox-panel-agent",
		"/etc/singbox-panel-agent",
		"/etc/systemd/system/singbox-panel-agent.service",
		"/etc/systemd/system/singbox-panel-agent-uninstall.service",
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
