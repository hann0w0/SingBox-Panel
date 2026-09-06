package panel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func installerFunction(t *testing.T, script, name string) string {
	t.Helper()
	start := strings.Index(script, name+"() {\n")
	if start < 0 {
		t.Fatalf("installer function %s is missing", name)
	}
	end := strings.Index(script[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("installer function %s is incomplete", name)
	}
	return script[start : start+end+3]
}

func TestInstallerEndpointOverridesPreserveOtherConfiguration(t *testing.T) {
	raw, err := os.ReadFile("../../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := installerFunction(t, string(raw), "yaml_double_quote") + "\n" + installerFunction(t, string(raw), "update_panel_endpoint_config")
	const original = `listen: "127.0.0.1:32334"
base_url: "https://old.example.com"
jwt_secret: "test-only-quoted\\value#preserved"
database:
  driver: sqlite
  dsn: "/custom/data/panel.db"
admin:
  email: "unchanged"
  password: ""
web_dir: "/custom/frontend"
`
	for _, tc := range []struct{ name, portFlag, originFlag, listen, origin string }{
		{"port-only", "1", "0", "127.0.0.1:34567", "https://old.example.com"},
		{"domain-only", "0", "1", "127.0.0.1:32334", "https://new.example.com"},
		{"both", "1", "1", "127.0.0.1:34567", "https://new.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "panel.yaml")
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			setup := "INSTALL_REAL=" + shellQuote(dir) + "\nPANEL_CONFIG=" + shellQuote(path) + "\n" +
				"PORT_PROVIDED=" + tc.portFlag + "\nBASE_URL_PROVIDED=" + tc.originFlag + "\n" +
				"PANEL_PORT=34567\nBASE_URL=https://new.example.com\nchown() { :; }\n"
			cmd := exec.Command("bash", "-c", "set -eu\n"+setup+script+"\nupdate_panel_endpoint_config\n")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("endpoint override failed: %v: %s", err, out)
			}
			updated, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var values map[string]any
			if err := yaml.Unmarshal(updated, &values); err != nil {
				t.Fatal(err)
			}
			if values["listen"] != tc.listen || values["base_url"] != tc.origin {
				t.Fatal("explicit port/domain did not match the persisted configuration")
			}
			if !strings.Contains(string(updated), original[strings.Index(original, "jwt_secret:"):]) {
				t.Fatal("endpoint override changed unrelated configuration")
			}
		})
	}
}
