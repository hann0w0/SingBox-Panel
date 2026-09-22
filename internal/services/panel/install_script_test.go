package panel

import (
	"os"
	"strings"
	"testing"
)

func TestBinaryInstallerRunsPanelAsRoot(t *testing.T) {
	raw, err := os.ReadFile("../../../install.sh")
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	script := string(raw)
	for _, required := range []string{
		"User=root",
		"Group=root",
		`chown -R root:root "$INSTALL_REAL/data" "$INSTALL_REAL/.update"`,
		`chmod 600 "$PANEL_CONFIG"`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("install.sh is missing root deployment contract %q", required)
		}
	}
	for _, forbidden := range []string{"PANEL_USER=", "PANEL_GROUP=", "ensure_panel_user"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("install.sh still contains low-privilege account logic %q", forbidden)
		}
	}
}

func TestBinaryInstallerGuardsDestructiveOperationsAndNetworkDownloads(t *testing.T) {
	raw, err := os.ReadFile("../../../install.sh")
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	script := string(raw)
	for _, required := range []string{
		`panel_process_running`,
		`refusing to uninstall while singbox-panel is still running`,
		`find "$INSTALL_DIR" -depth -delete || die`,
		`--connect-timeout 10`,
		`--max-time 300`,
		`--retry 3`,
		`service_enabled_by_install`,
		`systemctl disable "$SERVICE_NAME"`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("install.sh is missing safety contract %q", required)
		}
	}
	if strings.Contains(script, `for temp_file in ${TEMP_FILES+`) || strings.Contains(script, `for temp_dir in ${TEMP_DIRS+`) {
		t.Fatal("temporary-file cleanup still expands arrays without safe quoting")
	}
}
