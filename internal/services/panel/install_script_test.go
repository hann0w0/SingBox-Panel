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
