package agent

import (
	"os/exec"
	"strings"
	"testing"
)

func TestUninstallSingboxUsesPersistentTransactionalBackup(t *testing.T) {
	for _, required := range []string{
		"/var/lib/singbox-panel-agent/.singbox-uninstall.",
		`if [ "$committed" != "1" ]`,
		`systemctl is-active --quiet sing-box.service`,
		`rc-service sing-box status`,
		`rc-update del sing-box default`,
		`for pkg in sing-box-openrc sing-box-beta sing-box;`,
		`cp -a "$backup_root/config/." /etc/sing-box/`,
		`恢复副本保留在 $backup_root`,
	} {
		if !strings.Contains(uninstallSingboxScript, required) {
			t.Fatalf("uninstall script missing %q", required)
		}
	}
	if strings.Contains(uninstallSingboxScript, "mktemp -d /tmp/") {
		t.Fatal("uninstall backup still uses volatile /tmp")
	}
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(uninstallSingboxScript)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("uninstall script syntax: %v: %s", err, output)
	}
}

// The backup directory lives under /var/lib/singbox-panel-agent, which may not
// exist yet on a fresh or manually provisioned node. Creating it after mktemp
// made the script abort under set -e before any uninstall work happened.
func TestUninstallSingboxCreatesBackupParentBeforeMktemp(t *testing.T) {
	mkdir := strings.Index(uninstallSingboxScript, "mkdir -p /var/lib/singbox-panel-agent")
	mktemp := strings.Index(uninstallSingboxScript, "mktemp -d /var/lib/singbox-panel-agent/")
	if mkdir < 0 || mktemp < 0 {
		t.Fatalf("uninstall script missing backup parent creation (mkdir=%d mktemp=%d)", mkdir, mktemp)
	}
	if mkdir > mktemp {
		t.Fatal("uninstall script runs mktemp before creating /var/lib/singbox-panel-agent")
	}
}
