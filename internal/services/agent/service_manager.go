package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type serviceKind string

const (
	serviceKindSystemd serviceKind = "systemd"
	serviceKindOpenRC  serviceKind = "openrc"
)

type commandRunner func(context.Context, string, ...string) (string, error)

const openRCStartStopDaemon = "/sbin/start-stop-daemon"

const agentTaskStartupScript = `if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  SERVICE_MANAGER=systemd
  if ! command -v systemd-notify >/dev/null 2>&1; then
    printf '%s\n' 'systemd-notify is required to confirm Agent helper startup' >&2
    exit 1
  fi
  systemd-notify --ready --pid="$$" || exit 1
else
  SERVICE_MANAGER=openrc
fi
`

// serviceManager isolates the init-system-specific parts of managing the
// official sing-box installation. ApplyConfig and the command handlers work
// against this interface so Alpine uses OpenRC without weakening the existing
// systemd checks.
type serviceManager interface {
	Kind() serviceKind
	Prepare(context.Context) (string, error)
	Action(context.Context, string) (string, error)
	SetEnabled(context.Context, bool) (string, error)
	Active(context.Context) bool
	Enabled(context.Context) bool
	Status(context.Context) string
	MainPID(context.Context) (int, error)
	StartedAt(context.Context, int) (time.Time, error)
	LogCommand(lines int, follow bool) (string, []string)
}

func currentServiceManager() (serviceManager, error) {
	kind, err := detectServiceKind(pathExists("/run/systemd/system"), pathExists("/sbin/openrc-run"), commandExists)
	if err != nil {
		return nil, err
	}
	switch kind {
	case serviceKindSystemd:
		return systemdServiceManager{run: run}, nil
	case serviceKindOpenRC:
		clock, err := readProcessClockAt("/proc", time.Now)
		if err != nil {
			return nil, err
		}
		return openRCServiceManager{run: run, clock: clock}, nil
	default:
		return nil, fmt.Errorf("unsupported service manager %q", kind)
	}
}

func detectServiceKind(systemdRuntime, openrcRunner bool, exists func(string) bool) (serviceKind, error) {
	if systemdRuntime && exists("systemctl") {
		return serviceKindSystemd, nil
	}
	if openrcRunner && exists("rc-service") && exists("rc-update") {
		return serviceKindOpenRC, nil
	}
	return "", errors.New("unsupported init system: sing-box-panel-agent requires systemd or OpenRC")
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func launchOpenRCDetachedShell(ctx context.Context, script, pidFile string) error {
	out, err := run(ctx, openRCStartStopDaemon,
		"--start", "--background", "--make-pidfile", "--pidfile", pidFile,
		"--exec", "/bin/sh", "--", script)
	if err != nil {
		return fmt.Errorf("start detached OpenRC task: %w: %s", err, out)
	}
	return nil
}

func startSystemdAgentTask(ctx context.Context, unit string, runner commandRunner) (string, error) {
	return runner(ctx, "systemctl", "start", unit)
}

type systemdServiceManager struct {
	run      commandRunner
	procRoot string
}

func (m systemdServiceManager) procRootOrDefault() string {
	if m.procRoot != "" {
		return m.procRoot
	}
	return "/proc"
}

func (m systemdServiceManager) Kind() serviceKind { return serviceKindSystemd }

func (m systemdServiceManager) Prepare(ctx context.Context) (string, error) {
	return m.run(ctx, "systemctl", "daemon-reload")
}

func (m systemdServiceManager) Action(ctx context.Context, action string) (string, error) {
	if action == "start" || action == "restart" {
		// Pick up a unit file rewritten by an install/upgrade, so a restart really
		// starts the current definition instead of a stale cached one.
		_, _ = m.Prepare(ctx)
	}
	return m.run(ctx, "systemctl", action, ServiceName)
}

func (m systemdServiceManager) SetEnabled(ctx context.Context, enabled bool) (string, error) {
	action := "disable"
	if enabled {
		action = "enable"
	}
	return m.run(ctx, "systemctl", action, ServiceName)
}

func (m systemdServiceManager) Active(ctx context.Context) bool {
	out, err := m.run(ctx, "systemctl", "is-active", ServiceName)
	return err == nil && strings.TrimSpace(out) == "active"
}

func (m systemdServiceManager) Enabled(ctx context.Context) bool {
	out, err := m.run(ctx, "systemctl", "is-enabled", ServiceName)
	if err != nil {
		return false
	}
	s := strings.TrimSpace(out)
	return s == "enabled" || s == "enabled-runtime" || s == "alias"
}

func (m systemdServiceManager) Status(ctx context.Context) string {
	out, _ := m.run(ctx, "systemctl", "status", ServiceName, "--no-pager", "-l")
	return out
}

func (m systemdServiceManager) MainPID(ctx context.Context) (int, error) {
	out, err := m.run(ctx, "systemctl", "show", ServiceName, "--property=MainPID", "--value")
	if err != nil {
		return 0, fmt.Errorf("systemctl show MainPID: %w: %s", err, out)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("invalid MainPID %q: %w", strings.TrimSpace(out), err)
	}
	return pid, nil
}

func (m systemdServiceManager) StartedAt(ctx context.Context, pid int) (time.Time, error) {
	out, err := m.run(ctx, "busctl", "get-property", "org.freedesktop.systemd1", systemdUnitPath,
		"org.freedesktop.systemd1.Service", "ExecMainStartTimestamp")
	if err == nil {
		if ts, parseErr := parseSystemdTimestamp(out); parseErr == nil {
			return ts, nil
		}
	}
	// Minimal systemd hosts/containers without system D-Bus can still query PID 1
	// over /run/systemd/private via systemctl or read /proc/<pid>/stat directly.
	if showOut, showErr := m.run(ctx, "systemctl", "show", ServiceName, "--property=ExecMainStartTimestamp", "--value", "--timestamp=utc"); showErr == nil {
		if ts, parseErr := parseSystemctlUTCTimestamp(showOut); parseErr == nil {
			return ts, nil
		}
	}
	if pid > 0 {
		if clock, clockErr := readProcessClockAt(m.procRootOrDefault(), time.Now); clockErr == nil {
			if ts, statErr := readProcessStartTimeAt(m.procRootOrDefault(), pid, clock.bootLower); statErr == nil {
				return ts, nil
			}
		}
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("busctl get ExecMainStartTimestamp: %w: %s", err, out)
	}
	return parseSystemdTimestamp(out)
}

func parseSystemctlUTCTimestamp(out string) (time.Time, error) {
	s := strings.TrimSpace(out)
	if s == "" || s == "n/a" {
		return time.Time{}, errors.New("empty systemctl timestamp")
	}
	for _, layout := range []string{
		"Mon 2006-01-02 15:04:05.999999 UTC",
		"2006-01-02 15:04:05.999999 UTC",
		"Mon 2006-01-02 15:04:05 UTC",
		"2006-01-02 15:04:05 UTC",
	} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil && !t.IsZero() {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid systemctl ExecMainStartTimestamp %q", out)
}

func (m systemdServiceManager) LogCommand(lines int, follow bool) (string, []string) {
	args := []string{"-u", ServiceName, "-n", strconv.Itoa(lines)}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, "--no-pager")
	return "journalctl", args
}

func parseSystemdTimestamp(out string) (time.Time, error) {
	fields := strings.Fields(out)
	if len(fields) != 2 || fields[0] != "t" {
		return time.Time{}, fmt.Errorf("invalid ExecMainStartTimestamp %q", out)
	}
	micros, err := strconv.ParseUint(fields[1], 10, 64)
	const maxInt64 = int64(^uint64(0) >> 1)
	if err != nil || micros == 0 || micros > uint64(maxInt64/int64(time.Microsecond)) {
		return time.Time{}, fmt.Errorf("invalid ExecMainStartTimestamp %q", out)
	}
	return time.Unix(0, int64(micros)*int64(time.Microsecond)), nil
}
