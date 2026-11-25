package panel

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func TestBinaryInstallerHelpAndEarlyExitCleanup(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is required to execute install.sh")
	}

	scriptPath := filepath.Join("..", "..", "..", "install.sh")
	run := func(args ...string) (int, string, string) {
		t.Helper()
		cmd := exec.Command("bash", append([]string{scriptPath}, args...)...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		code := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else {
				t.Fatalf("run install.sh %v: %v", args, err)
			}
		}
		return code, stdout.String(), stderr.String()
	}

	for _, flag := range []string{"--help", "-h"} {
		code, stdout, stderr := run(flag)
		if code != 0 {
			t.Fatalf("install.sh %s exited %d: stderr=%s", flag, code, stderr)
		}
		if !strings.Contains(stdout, "SingBox Panel installer") {
			t.Fatalf("install.sh %s missing usage banner: %s", flag, stdout)
		}
		if strings.Contains(stderr, "unbound variable") {
			t.Fatalf("install.sh %s hit unbound variable in trap: %s", flag, stderr)
		}
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{args: []string{"--unknown-flag"}, want: "unknown option: --unknown-flag"},
		{args: []string{"--port"}, want: "--port requires a value"},
		{args: []string{"--purge"}, want: "--purge requires --uninstall"},
	} {
		// Pass --purge alone only works past root check; skip root-gated case if non-root.
		if tc.args[0] == "--purge" && os.Geteuid() != 0 {
			continue
		}
		code, _, stderr := run(tc.args...)
		if code == 0 {
			t.Fatalf("install.sh %v unexpectedly succeeded", tc.args)
		}
		if !strings.Contains(stderr, tc.want) || strings.Contains(stderr, "unbound variable") {
			t.Fatalf("install.sh %v stderr = %q, want %q without unbound variable", tc.args, stderr, tc.want)
		}
	}

	if os.Geteuid() != 0 {
		code, _, stderr := run()
		if code == 0 {
			t.Fatal("non-root install.sh unexpectedly succeeded")
		}
		if !strings.Contains(stderr, "run this installer as root") || strings.Contains(stderr, "unbound variable") {
			t.Fatalf("non-root early exit stderr = %q", stderr)
		}
	}
}

func TestBinaryInstallerUninstallAndInstallSuccessPaths(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is required to execute install.sh")
	}

	root := t.TempDir()
	rawScript, err := os.ReadFile(filepath.Join("..", "..", "..", "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}

	installDir := filepath.Join(root, "opt", "singbox-panel")
	panelBin := filepath.Join(root, "usr-local-bin-singbox-panel")
	serviceFile := filepath.Join(root, "etc-systemd-singbox-panel.service")
	binDir := filepath.Join(root, "bin")
	tmpDir := filepath.Join(root, "tmp")
	fixtureDir := filepath.Join(root, "fixtures")
	for _, dir := range []string{binDir, tmpDir, fixtureDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Redirect privileged filesystem targets and root guard into the test sandbox
	// while exercising the real script logic and EXIT cleanup trap unchanged.
	sandboxed := string(rawScript)
	sandboxed = strings.ReplaceAll(sandboxed, `PANEL_BIN="/usr/local/bin/singbox-panel"`, `PANEL_BIN="`+panelBin+`"`)
	sandboxed = strings.ReplaceAll(sandboxed, `SERVICE_FILE="/etc/systemd/system/singbox-panel.service"`, `SERVICE_FILE="`+serviceFile+`"`)
	sandboxed = strings.ReplaceAll(sandboxed, `[[ ${EUID:-$(id -u)} -eq 0 ]] || die "run this installer as root (for example: sudo ./install.sh)"`, `true`)
	sandboxed = strings.ReplaceAll(sandboxed, `install -d -m 700 -o root -g root`, `install -d -m 700`)

	sandboxScript := filepath.Join(root, "install.sh")
	if err := os.WriteFile(sandboxScript, []byte(sandboxed), 0o755); err != nil {
		t.Fatal(err)
	}

	// Build verifiable single-binary release fixtures.
	version := "v1.0.17"
	binaryBytes := []byte("#!/bin/sh\nexit 0\n")
	binaryPath := filepath.Join(fixtureDir, "singbox-panel-linux-amd64")
	if err := os.WriteFile(binaryPath, binaryBytes, 0o755); err != nil {
		t.Fatal(err)
	}

	checksums := fmt.Sprintf("%x  %s\n", sha256.Sum256(binaryBytes), "singbox-panel-linux-amd64")
	if err := os.WriteFile(filepath.Join(fixtureDir, "checksums.txt"), []byte(checksums), 0o644); err != nil {
		t.Fatal(err)
	}

	writeStub := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeStub("uname", "#!/bin/sh\nif [ \"$1\" = \"-m\" ]; then echo x86_64; else echo Linux; fi\n")
	writeStub("chown", "#!/bin/sh\nexit 0\n")
	writeStub("systemctl", "#!/bin/sh\nif [ \"$1\" = \"is-active\" ]; then exit 3; fi\nexit 0\n")
	writeStub("curl", fmt.Sprintf(`#!/bin/sh
out=""
url=""
for arg in "$@"; do :; done
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    http://*|https://*) url="$1"; shift ;;
    *) shift ;;
  esac
done
case "$url" in
  *"/api/health"|*"/api/ready") printf 'ok'; exit 0 ;;
  *)
    name="${url##*/}"
    if [ -n "$out" ] && [ -f "%s/$name" ]; then
      cp "%s/$name" "$out"
      exit 0
    fi
    exit 22
    ;;
esac
`, fixtureDir, fixtureDir))

	runSandboxed := func(args ...string) (string, string) {
		t.Helper()
		cmd := exec.Command("bash", append([]string{sandboxScript}, args...)...)
		cmd.Env = []string{
			"PATH=" + binDir + ":/usr/bin:/bin:/usr/sbin:/sbin",
			"TMPDIR=" + tmpDir,
			"HOME=" + root,
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("sandboxed install.sh %v failed: %v\nstdout=%s\nstderr=%s", args, err, stdout.String(), stderr.String())
		}
		if strings.Contains(stderr.String(), "unbound variable") {
			t.Fatalf("sandboxed install.sh %v hit unbound variable: %s", args, stderr.String())
		}
		return stdout.String(), stderr.String()
	}

	// 1. Successful install path.
	stdout, _ := runSandboxed(
		"--install-dir", installDir,
		"--base-url", "https://panel.example.com",
		"--version", version,
		"--non-interactive",
	)
	if !strings.Contains(stdout, "Installation complete.") {
		t.Fatalf("install output missing completion summary: %s", stdout)
	}
	if _, err := os.Stat(panelBin); err != nil {
		t.Fatalf("expected installed binary at %s: %v", panelBin, err)
	}
	if _, err := os.Stat(serviceFile); err != nil {
		t.Fatalf("expected systemd unit at %s: %v", serviceFile, err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "web")); !os.IsNotExist(err) {
		t.Fatalf("single-binary install must not create legacy web directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "dist")); !os.IsNotExist(err) {
		t.Fatalf("single-binary install must not create legacy dist directory: %v", err)
	}
	cfgBytes, err := os.ReadFile(filepath.Join(installDir, "panel.yaml"))
	if err != nil {
		t.Fatalf("read panel.yaml: %v", err)
	}
	if strings.Contains(string(cfgBytes), "web_dir:") || strings.Contains(string(cfgBytes), "agents_dir:") {
		t.Fatalf("fresh single-binary config must not include web_dir/agents_dir: %s", cfgBytes)
	}
	if leftover, err := filepath.Glob(filepath.Join(tmpDir, "singbox-panel*")); err != nil || len(leftover) > 0 {
		t.Fatalf("expected temp files cleaned up, found %v (err=%v)", leftover, err)
	}
	if leftover, err := filepath.Glob(filepath.Join(installDir, ".rollback.*")); err != nil || len(leftover) > 0 {
		t.Fatalf("expected rollback dirs cleaned up, found %v (err=%v)", leftover, err)
	}

	// 1b. Legacy upgrade removes leftover web/ and dist/ directories and prunes
	// default web_dir/agents_dir lines while preserving database and custom keys.
	if err := os.MkdirAll(filepath.Join(installDir, "web", "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "web", "dist", "index.html"), []byte("old-web"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(installDir, "dist", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "dist", "agents", "singbox-panel-agent-linux-amd64"), []byte("old-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "data", "keep.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	realInstallDir, err := filepath.EvalSymlinks(installDir)
	if err != nil {
		t.Fatal(err)
	}
	legacyCfg := string(cfgBytes) +
		fmt.Sprintf("agents_dir: %q\nweb_dir: %q\n", filepath.Join(realInstallDir, "dist", "agents"), filepath.Join(realInstallDir, "web", "dist"))
	if err := os.WriteFile(filepath.Join(installDir, "panel.yaml"), []byte(legacyCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _ = runSandboxed(
		"--install-dir", installDir,
		"--version", version,
		"--non-interactive",
	)
	if !strings.Contains(stdout, "Installation complete.") {
		t.Fatalf("upgrade output missing completion summary: %s", stdout)
	}
	if _, err := os.Stat(filepath.Join(installDir, "web")); !os.IsNotExist(err) {
		t.Fatalf("legacy web directory survived upgrade: %v", err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "dist")); !os.IsNotExist(err) {
		t.Fatalf("legacy dist/agents directory survived upgrade: %v", err)
	}
	upgradedCfg, err := os.ReadFile(filepath.Join(installDir, "panel.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(upgradedCfg), "web_dir:") || strings.Contains(string(upgradedCfg), "agents_dir:") {
		t.Fatalf("legacy default web_dir/agents_dir lines survived upgrade: %s", upgradedCfg)
	}
	if _, err := os.Stat(filepath.Join(installDir, "data", "keep.db")); err != nil {
		t.Fatalf("upgrade must preserve database: %v", err)
	}

	// 2. Non-purge uninstall path (keeps data/ and panel.yaml, TEMP_FILES/TEMP_DIRS empty).
	stdout, _ = runSandboxed("--install-dir", installDir, "--uninstall", "--yes")
	if !strings.Contains(stdout, "SingBox Panel uninstalled.") {
		t.Fatalf("uninstall output missing summary: %s", stdout)
	}
	if _, err := os.Stat(panelBin); !os.IsNotExist(err) {
		t.Fatalf("expected binary removed after uninstall")
	}
	if _, err := os.Stat(filepath.Join(installDir, "data", "keep.db")); err != nil {
		t.Fatalf("non-purge uninstall must preserve data: %v", err)
	}

	// 3. Purge uninstall path (removes entire installDir).
	stdout, _ = runSandboxed("--install-dir", installDir, "--uninstall", "--purge", "--yes")
	if !strings.Contains(stdout, "SingBox Panel completely uninstalled.") {
		t.Fatalf("purge uninstall output missing summary: %s", stdout)
	}
	if _, err := os.Stat(installDir); !os.IsNotExist(err) {
		t.Fatalf("purge uninstall must remove %s", installDir)
	}
}
