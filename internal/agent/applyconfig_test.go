package agent

import (
	"os"
	"path/filepath"
	"testing"
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
