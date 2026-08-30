package agent

import (
	"os/exec"
	"strings"
	"testing"
)

func TestUninstallSingboxPreservesConfigurationAndState(t *testing.T) {
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(uninstallSingboxScript)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("uninstall script syntax: %v: %s", err, output)
	}
	for _, forbidden := range []string{"rm -rf /etc/sing-box", "rm -f /etc/sing-box", "rm -rf /var/lib/sing-box", "rm -f /var/lib/sing-box"} {
		if strings.Contains(uninstallSingboxScript, forbidden) {
			t.Fatalf("uninstall script deletes preserved data with %q", forbidden)
		}
	}
	if !strings.Contains(uninstallSingboxScript, "apt-get remove") || !strings.Contains(uninstallSingboxScript, "dnf remove") {
		t.Fatal("uninstall script does not cover supported package managers")
	}
	if !strings.Contains(uninstallSingboxScript, "cp -a /etc/sing-box") || !strings.Contains(uninstallSingboxScript, "cp -a /var/lib/sing-box") {
		t.Fatal("uninstall script does not snapshot preserved directories")
	}
}
