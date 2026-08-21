package agent

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"
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
