package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMoveStrayConfigsCanRestoreOnlyCurrentApply(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "config.json"), "main")
	writeTestFile(t, filepath.Join(dir, "extra.json"), "extra")
	writeTestFile(t, filepath.Join(dir, "notes.txt"), "notes")
	writeTestFile(t, filepath.Join(dir, "disabled", "older", "extra.json"), "older")

	moved, err := moveStrayConfigsAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved.files) != 1 || moved.backupDir == "" {
		t.Fatalf("move journal = %+v", moved)
	}
	if _, err := os.Stat(filepath.Join(dir, "extra.json")); !os.IsNotExist(err) {
		t.Fatalf("extra.json still active: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(moved.backupDir, "extra.json")); err != nil || string(got) != "extra" {
		t.Fatalf("moved file = %q, err=%v", got, err)
	}
	if err := moved.restore(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "extra.json")); err != nil || string(got) != "extra" {
		t.Fatalf("restored file = %q, err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "disabled", "older", "extra.json")); err != nil || string(got) != "older" {
		t.Fatalf("older backup changed = %q, err=%v", got, err)
	}
	if _, err := os.Stat(moved.backupDir); !os.IsNotExist(err) {
		t.Fatalf("rollback directory still exists: %v", err)
	}
}

func TestMoveStrayConfigsUsesUniqueBackupDirectories(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "config.json"), "main")
	writeTestFile(t, filepath.Join(dir, "extra.json"), "first")
	first, err := moveStrayConfigsAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "extra.json"), "second")
	second, err := moveStrayConfigsAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.backupDir == second.backupDir {
		t.Fatalf("backup directory reused: %s", first.backupDir)
	}
	for path, want := range map[string]string{
		filepath.Join(first.backupDir, "extra.json"):  "first",
		filepath.Join(second.backupDir, "extra.json"): "second",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, err=%v; want %q", path, got, err, want)
		}
	}
}

func TestCopyFileOnceNeverOverwritesOriginal(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "config.json")
	dst := filepath.Join(dir, "config.json.orig")
	writeTestFile(t, src, "first")
	if err := copyFileOnce(src, dst); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, src, "second")
	if err := copyFileOnce(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "first" {
		t.Fatalf("original backup = %q, err=%v; want first", got, err)
	}
}

func TestCopyFileUsesPrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.json")
	dst := filepath.Join(dir, "dst.json")
	if err := os.WriteFile(src, []byte(`{"password":"secret"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("copied config mode = %o, want 600", info.Mode().Perm())
	}
}

func TestVerifyManagedConfigFileRequiresMatchingHashAndNewerService(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	contents := []byte(`{"log":{"level":"info"}}`)
	writeTestFile(t, path, string(contents))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(contents)
	if err := verifyManagedConfigFile(path, hash, info.ModTime().Add(time.Second)); err != nil {
		t.Fatalf("valid config evidence rejected: %v", err)
	}
	if err := verifyManagedConfigFile(path, sha256.Sum256([]byte("different")), info.ModTime().Add(time.Second)); err == nil {
		t.Fatal("mismatched config hash was accepted")
	}
	if err := verifyManagedConfigFile(path, hash, info.ModTime()); err == nil {
		t.Fatal("service not started after config was accepted")
	}
	if err := verifyManagedConfigFile(path, hash, info.ModTime().Add(-time.Second)); err == nil {
		t.Fatal("service older than config was accepted")
	}
}

func TestProcessArgsLoadManagedConfig(t *testing.T) {
	for _, args := range [][]string{
		{"/usr/bin/sing-box", "-C", "/etc/sing-box", "run"},
		{"/usr/bin/sing-box", "--config-directory=/etc/sing-box/", "run"},
		{"/usr/bin/sing-box", "-c", "/etc/sing-box/config.json", "run"},
		{"/usr/bin/sing-box", "--config=/etc/sing-box/config.json", "run"},
	} {
		if !processArgsLoadManagedConfig(args) {
			t.Fatalf("managed config arguments rejected: %q", args)
		}
	}
	for _, args := range [][]string{
		{"/usr/bin/sing-box", "run"},
		{"/usr/bin/sing-box", "-C", "/tmp/sing-box", "run"},
		{"/usr/bin/sing-box", "-c", "/tmp/config.json", "run"},
	} {
		if processArgsLoadManagedConfig(args) {
			t.Fatalf("unmanaged config arguments accepted: %q", args)
		}
	}
}

func TestParseSystemdTimestamp(t *testing.T) {
	got, err := parseSystemdTimestamp("t 1787285641404504")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Unix(0, 1787285641404504*int64(time.Microsecond)); !got.Equal(want) {
		t.Fatalf("timestamp = %s; want %s", got, want)
	}
	for _, invalid := range []string{"", "t 0", "s 1787285641404504", "t nope", "t 1 extra"} {
		if _, err := parseSystemdTimestamp(invalid); err == nil {
			t.Fatalf("invalid timestamp %q was accepted", invalid)
		}
	}
}

func TestConfigApplyPreservesServiceEnablement(t *testing.T) {
	for _, tc := range []struct {
		name     string
		previous serviceState
		want     string
	}{
		{name: "active enabled", previous: serviceState{active: true, enabled: true}, want: "restart"},
		{name: "active disabled", previous: serviceState{active: true, enabled: false}, want: "restart"},
		{name: "inactive enabled", previous: serviceState{active: false, enabled: true}, want: "start"},
		{name: "inactive disabled", previous: serviceState{active: false, enabled: false}, want: "start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := configApplyServiceAction(tc.previous); got != tc.want {
				t.Fatalf("action = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIndentConfigJSONRestoresReadableConfigVerbatim(t *testing.T) {
	// The panel's envelope compacts nested raw JSON, so the agent receives one
	// long line; the installed file must still be readable over SSH.
	compact := []byte(`{"log":{"level":"info"},"inbounds":[{"tag":"SS","listen_port":443,"password":"a/b\u0041"}],"big":[18446744073709551615]}`)
	got := indentConfigJSON(compact)
	if n := bytes.Count(got, []byte("\n")); n < 6 {
		t.Fatalf("indented config has %d newlines, want a multi-line document:\n%s", n, got)
	}
	if !bytes.HasSuffix(got, []byte("\n")) {
		t.Fatalf("indented config does not end with a newline: %q", got)
	}
	// Re-marshalling through any would rewrite the big integer and the escaped
	// string; both must survive exactly as they were sent.
	for _, want := range []string{"18446744073709551615", `"password": "a/b\u0041"`} {
		if !bytes.Contains(got, []byte(want)) {
			t.Fatalf("indent rewrote %q:\n%s", want, got)
		}
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, got); err != nil {
		t.Fatalf("indented config is not valid JSON: %v", err)
	}
	if !bytes.Equal(compacted.Bytes(), compact) {
		t.Fatalf("indent changed the document:\n got %s\nwant %s", compacted.Bytes(), compact)
	}
	// Idempotent: normalising an already-indented config must be a no-op, or every
	// panel reconnect would look like a config change and restart sing-box.
	if second := indentConfigJSON(got); !bytes.Equal(second, got) {
		t.Fatalf("indent is not idempotent:\nfirst  %q\nsecond %q", got, second)
	}
}

func TestIndentConfigJSONLeavesMalformedInputUntouched(t *testing.T) {
	// sing-box's own `check` must stay the authority on validity, so a config this
	// function cannot parse passes through unchanged and fails there instead.
	for _, raw := range []string{"", "   ", "not json", `{"unterminated":`, `{"a":1,}`} {
		if got := indentConfigJSON([]byte(raw)); string(got) != raw {
			t.Fatalf("indentConfigJSON(%q) = %q; want unchanged", raw, got)
		}
	}
}

func TestIndentConfigJSONRoundTripsTheWireEnvelope(t *testing.T) {
	// End-to-end guard for why this function exists: the panel renders an indented
	// config, the envelope's json.Marshal compacts the nested raw message, and the
	// agent must hand the operator back exactly what the panel rendered. Any drift
	// here would make an unchanged config look changed on every push.
	rendered, err := json.MarshalIndent(json.RawMessage(`{"log":{"level":"info"},"inbounds":[{"tag":"SS","password":"p"}]}`), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	env, err := protocol.NewEnvelope(protocol.CmdApplyConfig, "1", protocol.ApplyConfigCmd{Config: rendered})
	if err != nil {
		t.Fatal(err)
	}
	var received protocol.ApplyConfigCmd
	if err := env.Decode(&received); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(received.Config, []byte("\n")) {
		t.Fatal("test premise broken: the envelope no longer compacts nested config JSON")
	}
	want := make([]byte, 0, len(rendered)+1)
	want = append(want, rendered...)
	want = append(want, '\n')
	if got := indentConfigJSON(received.Config); !bytes.Equal(got, want) {
		t.Fatalf("agent would write a different document than the panel rendered:\n got %q\nwant %q", got, want)
	}
}

func TestPrepareConfigBytesReturnsTheBytesApplyConfigWillHash(t *testing.T) {
	// ApplyConfig hashes, validates and installs exactly what this returns, so the
	// formatting has to happen here: moving it after the hash would make sing-box
	// check and verifyManagedConfigFile disagree about the bytes on disk.
	compact := []byte(`{"log":{"level":"info"},"inbounds":[{"tag":"SS"}]}`)
	got, err := prepareConfigBytes(compact)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, indentConfigJSON(compact)) {
		t.Fatalf("prepareConfigBytes returned %q, want the formatted config %q", got, indentConfigJSON(compact))
	}
	if bytes.Equal(got, compact) {
		t.Fatal("prepareConfigBytes returned the compact input unformatted")
	}
}

func TestPrepareConfigBytesRejectsConfigsOverTheLimit(t *testing.T) {
	if _, err := prepareConfigBytes(nil); err == nil {
		t.Fatal("an empty config was accepted")
	}
	if _, err := prepareConfigBytes(make([]byte, maxConfigSize+1)); err == nil {
		t.Fatal("a config over the limit was accepted")
	}
	// Indenting only grows a config, so one a few bytes under the limit formats
	// past it. Rejecting that up front beats installing it and then failing the
	// verification step that re-hashes the file against the same limit.
	inner := make([]byte, 0, maxConfigSize-512)
	inner = append(inner, '[')
	for len(inner)+2 <= maxConfigSize-512 {
		inner = append(inner, '0', ',')
	}
	inner[len(inner)-1] = ']'
	if len(inner) > maxConfigSize {
		t.Fatalf("test premise broken: compact input is %d bytes", len(inner))
	}
	_, err := prepareConfigBytes(inner)
	if err == nil {
		t.Fatal("a config that grows past the limit while being formatted was accepted")
	}
	if !strings.Contains(err.Error(), "indented config exceeds") {
		t.Fatalf("unexpected error for an oversized formatted config: %v", err)
	}
}
