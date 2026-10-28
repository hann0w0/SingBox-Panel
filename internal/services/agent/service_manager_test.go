package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSystemdServiceManagerUsesNativeCommands(t *testing.T) {
	ctx := context.Background()
	var calls [][]string
	manager := systemdServiceManager{run: func(_ context.Context, command string, args ...string) (string, error) {
		calls = append(calls, append([]string{command}, args...))
		switch command + " " + strings.Join(args, " ") {
		case "systemctl is-active sing-box":
			return "active", nil
		case "systemctl is-enabled sing-box":
			return "enabled", nil
		case "systemctl show sing-box --property=MainPID --value":
			return "4321", nil
		case "busctl get-property org.freedesktop.systemd1 /org/freedesktop/systemd1/unit/sing_2dbox_2eservice org.freedesktop.systemd1.Service ExecMainStartTimestamp":
			return "t 1787285641404504", nil
		default:
			return "ok", nil
		}
	}}

	if !manager.Active(ctx) || !manager.Enabled(ctx) {
		t.Fatal("systemd active/enabled state was not recognized")
	}
	pid, err := manager.MainPID(ctx)
	if err != nil || pid != 4321 {
		t.Fatalf("MainPID = %d, err=%v", pid, err)
	}
	started, err := manager.StartedAt(ctx, pid)
	if err != nil || started.UnixMicro() != 1787285641404504 {
		t.Fatalf("StartedAt = %s, err=%v", started, err)
	}
	if _, err := manager.Action(ctx, "restart"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	command, args := manager.LogCommand(25, true)
	if command != "journalctl" || !reflect.DeepEqual(args, []string{"-u", "sing-box", "-n", "25", "-f", "--no-pager"}) {
		t.Fatalf("systemd log command = %s %q", command, args)
	}
	wantTail := []string{"systemctl", "disable", "sing-box"}
	if !containsCommandCall(calls, wantTail) {
		t.Fatalf("systemd calls missing %q: %q", wantTail, calls)
	}
}

func TestPathExistsAcceptsRuntimeDirectories(t *testing.T) {
	if !pathExists(t.TempDir()) {
		t.Fatal("runtime directory was treated as absent")
	}
}

func TestDetectServiceKind(t *testing.T) {
	allCommands := func(string) bool { return true }
	if kind, err := detectServiceKind(true, true, allCommands); err != nil || kind != serviceKindSystemd {
		t.Fatalf("systemd detection = %q, err=%v", kind, err)
	}
	openRCCommands := func(name string) bool { return name == "rc-service" || name == "rc-update" }
	if kind, err := detectServiceKind(false, true, openRCCommands); err != nil || kind != serviceKindOpenRC {
		t.Fatalf("OpenRC detection = %q, err=%v", kind, err)
	}
	if _, err := detectServiceKind(false, true, func(string) bool { return false }); err == nil {
		t.Fatal("missing init commands were accepted")
	}
}

func TestOpenRCServiceManagerUsesNativeCommandsAndLogFile(t *testing.T) {
	ctx := context.Background()
	var calls [][]string
	manager := openRCServiceManager{runlevelRoot: t.TempDir(), run: func(_ context.Context, command string, args ...string) (string, error) {
		calls = append(calls, append([]string{command}, args...))
		if command == "rc-service" && len(args) == 2 && args[1] == "status" {
			return " * status: started", nil
		}
		return "ok", nil
	}}

	if !manager.Active(ctx) {
		t.Fatal("OpenRC active service was not recognized")
	}
	if _, err := manager.SetEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Action(ctx, "stop"); err != nil {
		t.Fatal(err)
	}
	if !containsCommandCall(calls, []string{"rc-update", "add", "sing-box", "default"}) {
		t.Fatalf("OpenRC enable call missing: %q", calls)
	}
	command, args := manager.LogCommand(40, true)
	if command != "tail" || !reflect.DeepEqual(args, []string{"-n", "40", "-f", openRCSingboxLog}) {
		t.Fatalf("OpenRC log command = %s %q", command, args)
	}
}

func TestAppendOpenRCLoggingPreservesCustomSettings(t *testing.T) {
	empty := appendOpenRCLogging(nil)
	if !strings.Contains(string(empty), "output_log="+openRCSingboxLog) ||
		!strings.Contains(string(empty), "error_log="+openRCSingboxLog) {
		t.Fatalf("empty OpenRC config was not populated:\n%s", empty)
	}

	custom := []byte("# output_log=/tmp/ignored\noutput_log=/tmp/custom.log\n")
	updated := appendOpenRCLogging(custom)
	if strings.Count(string(updated), "output_log=") != 2 { // one comment plus one setting
		t.Fatalf("custom output_log was overwritten:\n%s", updated)
	}
	if !strings.Contains(string(updated), "error_log="+openRCSingboxLog) {
		t.Fatalf("missing error_log was not added:\n%s", updated)
	}

	complete := []byte("output_log=/tmp/out.log\nerror_log=/tmp/err.log\n")
	if got := appendOpenRCLogging(complete); string(got) != string(complete) {
		t.Fatalf("complete OpenRC logging config changed:\n%s", got)
	}
	if got := openRCLogPathFromConfig(string(complete)); got != "/tmp/out.log" {
		t.Fatalf("custom OpenRC log path = %q", got)
	}
	if got := openRCLogPathFromConfig("output_logger=/usr/bin/logger\n"); got != openRCSingboxLog {
		t.Fatalf("logger-only config used wrong log path: %q", got)
	}
}

func TestOpenRCSingboxServiceScriptIsValidShell(t *testing.T) {
	script := openRCSingboxServiceScript("/usr/bin/sing-box")
	for _, required := range []string{openRCManagedMarker, "supervise-daemon", `command="/usr/bin/sing-box"`, "check -C /etc/sing-box"} {
		if !strings.Contains(script, required) {
			t.Fatalf("OpenRC service missing %q:\n%s", required, script)
		}
	}
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("OpenRC service shell syntax: %v: %s", err, out)
	}
}

func TestFindManagedSingboxPIDAt(t *testing.T) {
	procRoot := t.TempDir()
	writeProcessFixture(t, procRoot, 101, "/usr/bin/sing-box\x00run\x00-C\x00/etc/sing-box\x00", "S")
	writeProcessFixture(t, procRoot, 102, "/usr/bin/other\x00run\x00-C\x00/etc/sing-box\x00", "S")
	writeProcessFixture(t, procRoot, 103, "/usr/bin/sing-box\x00run\x00-c\x00/tmp/other.json\x00", "S")

	pid, err := findManagedSingboxPIDAt(procRoot)
	if err != nil || pid != 101 {
		t.Fatalf("managed PID = %d, err=%v", pid, err)
	}

	writeProcessFixture(t, procRoot, 104, "/usr/bin/sing-box\x00run\x00-C\x00/etc/sing-box\x00", "S")
	if _, err := findManagedSingboxPIDAt(procRoot); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("multiple managed processes error = %v", err)
	}
}

func TestReadProcessStartTimeAt(t *testing.T) {
	procRoot := t.TempDir()
	fields := make([]string, 20)
	fields[0] = "S"
	for i := 1; i < len(fields)-1; i++ {
		fields[i] = "0"
	}
	fields[len(fields)-1] = "12345"
	writeTestFile(t, filepath.Join(procRoot, "321", "stat"), "321 (sing box) "+strings.Join(fields, " ")+"\n")
	bootLower := time.Unix(1700000000, 900000000)
	got, err := readProcessStartTimeAt(procRoot, 321, bootLower)
	if err != nil {
		t.Fatal(err)
	}
	want := bootLower.Add(12345 * 10 * time.Millisecond)
	if !got.Equal(want) {
		t.Fatalf("process start = %s; want %s", got, want)
	}
	if ticks, err := parseProcessStartTicks("321 (sing box) " + strings.Join(fields, " ")); err != nil || ticks != 12345 {
		t.Fatalf("parse start ticks = %d, err=%v", ticks, err)
	}
	if _, err := readProcessStartTimeAt(procRoot, 321, time.Time{}); err == nil {
		t.Fatal("missing boot-time lower bound was accepted")
	}
}

func writeProcessFixture(t *testing.T, procRoot string, pid int, cmdline, state string) {
	t.Helper()
	dir := filepath.Join(procRoot, intString(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(intString(pid)+" (sing-box) "+state+" 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func intString(value int) string {
	return strconv.Itoa(value)
}

func containsCommandCall(calls [][]string, want []string) bool {
	for _, call := range calls {
		if reflect.DeepEqual(call, want) {
			return true
		}
	}
	return false
}
