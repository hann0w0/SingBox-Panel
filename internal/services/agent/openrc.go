package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	openRCSingboxInit   = "/etc/init.d/sing-box"
	openRCSingboxConf   = "/etc/conf.d/sing-box"
	openRCSingboxLog    = "/var/log/sing-box.log"
	openRCManagedMarker = "Managed by SingBox Panel"
)

type openRCServiceManager struct {
	run          commandRunner
	runlevelRoot string
	clock        processStartClock
}

func (m openRCServiceManager) Kind() serviceKind { return serviceKindOpenRC }

func (m openRCServiceManager) Prepare(_ context.Context) (string, error) {
	if err := ensureOpenRCSingboxService(singboxBinary()); err != nil {
		return "", err
	}
	if err := ensureOpenRCLogging(); err != nil {
		return "", err
	}
	return "", nil
}

func (m openRCServiceManager) Action(ctx context.Context, action string) (string, error) {
	if action == "enable" || action == "disable" {
		return m.SetEnabled(ctx, action == "enable")
	}
	if action == "start" || action == "restart" {
		if out, err := m.Prepare(ctx); err != nil {
			return out, err
		}
	}
	return m.run(ctx, "rc-service", ServiceName, action)
}

func (m openRCServiceManager) SetEnabled(ctx context.Context, enabled bool) (string, error) {
	_, statErr := os.Stat(m.runlevelPath())
	alreadyEnabled := statErr == nil
	if enabled == alreadyEnabled {
		return "", nil
	}
	action := "del"
	if enabled {
		action = "add"
	}
	return m.run(ctx, "rc-update", action, ServiceName, "default")
}

func (m openRCServiceManager) Active(ctx context.Context) bool {
	_, err := m.run(ctx, "rc-service", ServiceName, "status")
	return err == nil
}

func (m openRCServiceManager) Enabled(_ context.Context) bool {
	_, err := os.Stat(m.runlevelPath())
	return err == nil
}

func (m openRCServiceManager) runlevelPath() string {
	root := m.runlevelRoot
	if root == "" {
		root = "/etc/runlevels/default"
	}
	return filepath.Join(root, ServiceName)
}

func (m openRCServiceManager) Status(ctx context.Context) string {
	out, _ := m.run(ctx, "rc-service", ServiceName, "status")
	return out
}

func (m openRCServiceManager) MainPID(context.Context) (int, error) {
	return findManagedSingboxPIDAt("/proc")
}

func (m openRCServiceManager) StartedAt(_ context.Context, pid int) (time.Time, error) {
	return readProcessStartTimeAt("/proc", pid, m.clock.bootLower)
}

func (m openRCServiceManager) waitForConfigStart(ctx context.Context) error {
	if m.clock.bootLower.IsZero() || m.clock.uncertainty <= 0 {
		return errors.New("OpenRC process clock is unavailable")
	}
	timer := time.NewTimer(m.clock.uncertainty)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m openRCServiceManager) LogCommand(lines int, follow bool) (string, []string) {
	args := []string{"-n", strconv.Itoa(lines)}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, openRCLogPath())
	return "tail", args
}

func openRCLogPath() string {
	raw, err := os.ReadFile(openRCSingboxConf)
	if err != nil {
		return openRCSingboxLog
	}
	return openRCLogPathFromConfig(string(raw))
}

func openRCLogPathFromConfig(raw string) string {
	for _, key := range []string{"output_log", "error_log"} {
		value, ok := openRCLogSetting(raw, key)
		if ok && filepath.IsAbs(value) {
			return value
		}
	}
	return openRCSingboxLog
}

func ensureOpenRCSingboxService(binary string) error {
	if _, err := os.Stat(openRCSingboxInit); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect OpenRC sing-box service: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(openRCSingboxInit), 0o755); err != nil {
		return fmt.Errorf("create OpenRC service directory: %w", err)
	}
	script := openRCSingboxServiceScript(binary)
	if err := writeAtomicFile(openRCSingboxInit, []byte(script), 0o755); err != nil {
		return fmt.Errorf("write OpenRC sing-box service: %w", err)
	}
	return nil
}

func openRCSingboxServiceScript(binary string) string {
	return fmt.Sprintf(`#!/sbin/openrc-run
# %s. This service is removed only when it still carries this marker.
description="sing-box service"
supervisor="supervise-daemon"
command=%q
command_args="run --disable-color -C /etc/sing-box"
respawn_delay=5
respawn_max=0

depend() {
	after net dns
}

start_pre() {
	"$command" check -C /etc/sing-box
}
`, openRCManagedMarker, binary)
}

func ensureOpenRCLogging() error {
	raw, err := os.ReadFile(openRCSingboxConf)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read OpenRC sing-box configuration: %w", err)
	}
	updated := appendOpenRCLogging(raw)
	if string(updated) == string(raw) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(openRCSingboxConf), 0o755); err != nil {
		return fmt.Errorf("create OpenRC configuration directory: %w", err)
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(openRCSingboxConf); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := writeAtomicFile(openRCSingboxConf, updated, mode); err != nil {
		return fmt.Errorf("configure OpenRC sing-box logs: %w", err)
	}
	return nil
}

func appendOpenRCLogging(raw []byte) []byte {
	data := string(raw)
	missingOutput := !openRCLogSettingPresent(data, "output_log")
	missingError := !openRCLogSettingPresent(data, "error_log")
	if !missingOutput && !missingError {
		return raw
	}
	var b strings.Builder
	b.Write(raw)
	if len(data) > 0 && !strings.HasSuffix(data, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("# BEGIN ")
	b.WriteString(openRCManagedMarker)
	b.WriteString(" log forwarding\n")
	if missingOutput {
		b.WriteString("output_log=" + openRCSingboxLog + "\n")
	}
	if missingError {
		b.WriteString("error_log=" + openRCSingboxLog + "\n")
	}
	b.WriteString("# END ")
	b.WriteString(openRCManagedMarker)
	b.WriteString(" log forwarding\n")
	return []byte(b.String())
}

func openRCLogSettingPresent(raw, key string) bool {
	_, ok := openRCLogSetting(raw, key)
	return ok
}

func openRCLogSetting(raw, key string) (string, bool) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		if !strings.HasPrefix(line, key) {
			continue
		}
		remainder := strings.TrimSpace(strings.TrimPrefix(line, key))
		if !strings.HasPrefix(remainder, "=") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(remainder, "="))
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		return value, true
	}
	return "", false
}

func findManagedSingboxPIDAt(procRoot string) (int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0, fmt.Errorf("scan processes for OpenRC sing-box: %w", err)
	}
	matches := make([]int, 0, 1)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		args := splitNULTerminated(raw)
		if len(args) == 0 || filepath.Base(args[0]) != "sing-box" || !processArgsLoadManagedConfig(args) {
			continue
		}
		if processIsZombieAt(procRoot, pid) {
			continue
		}
		matches = append(matches, pid)
	}
	switch len(matches) {
	case 0:
		return 0, errors.New("OpenRC reports sing-box active but no managed sing-box process was found")
	case 1:
		return matches[0], nil
	default:
		return 0, fmt.Errorf("multiple managed sing-box processes found: %v", matches)
	}
}

func processIsZombieAt(procRoot string, pid int) bool {
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	state, err := parseProcessState(string(raw))
	return err == nil && state == "Z"
}

func splitNULTerminated(raw []byte) []string {
	parts := strings.Split(string(raw), "\x00")
	args := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			args = append(args, part)
		}
	}
	return args
}

const processClockTick = 10 * time.Millisecond

type processStartClock struct {
	bootLower   time.Time
	uncertainty time.Duration
}

// /proc/stat btime loses up to a second. Bound boot time with a wall-clock
// sample around /proc/uptime instead, accounting for its centisecond rounding.
// Reuse this anchor throughout an apply so process identity stays stable.
func readProcessClockAt(procRoot string, now func() time.Time) (processStartClock, error) {
	before := now().Round(0)
	raw, err := os.ReadFile(filepath.Join(procRoot, "uptime"))
	after := now().Round(0)
	if err != nil {
		return processStartClock{}, fmt.Errorf("read system uptime: %w", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return processStartClock{}, errors.New("system uptime is missing")
	}
	uptime, err := time.ParseDuration(fields[0] + "s")
	if err != nil || uptime < 0 || after.Before(before) {
		return processStartClock{}, errors.New("invalid system uptime clock sample")
	}
	return processStartClock{
		bootLower: before.Add(-uptime - processClockTick),
		// Both uptime and process start ticks round down. Waiting this long
		// after writing the config keeps a new process's lower bound newer too.
		uncertainty: after.Sub(before) + 2*processClockTick,
	}, nil
}

func readProcessStartTimeAt(procRoot string, pid int, bootLower time.Time) (time.Time, error) {
	if bootLower.IsZero() {
		return time.Time{}, errors.New("system boot-time lower bound is missing")
	}
	stat, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return time.Time{}, fmt.Errorf("read process stat: %w", err)
	}
	ticks, err := parseProcessStartTicks(string(stat))
	if err != nil {
		return time.Time{}, err
	}
	// Linux exposes /proc stat timestamps in USER_HZ, which is fixed at 100.
	if ticks > uint64((1<<63-1)/processClockTick) {
		return time.Time{}, errors.New("process start time exceeds duration range")
	}
	return bootLower.Add(time.Duration(ticks) * processClockTick), nil
}

func parseProcessStartTicks(raw string) (uint64, error) {
	closeParen := strings.LastIndexByte(raw, ')')
	if closeParen < 0 || closeParen+1 >= len(raw) {
		return 0, errors.New("invalid process stat format")
	}
	fields := strings.Fields(raw[closeParen+1:])
	// fields[0] is field 3 (state); process start time is field 22.
	if len(fields) <= 19 {
		return 0, errors.New("process stat is missing start time")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid process start time %q", fields[19])
	}
	return ticks, nil
}

func parseProcessState(raw string) (string, error) {
	closeParen := strings.LastIndexByte(raw, ')')
	if closeParen < 0 {
		return "", errors.New("invalid process stat format")
	}
	fields := strings.Fields(raw[closeParen+1:])
	if len(fields) == 0 {
		return "", errors.New("process stat is missing state")
	}
	return fields[0], nil
}
