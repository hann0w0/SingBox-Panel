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
