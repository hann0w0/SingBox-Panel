package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func processExited(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return true
	}
	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if errors.Is(err, os.ErrNotExist) {
			// Re-check signal probe in case init reaped the zombie between checks.
			return syscall.Kill(pid, 0) != nil
		}
		if err == nil {
			state, parseErr := parseProcessState(string(raw))
			return parseErr == nil && (state == "Z" || state == "X" || state == "x")
		}
	}
	return false
}

func waitProcessExited(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if processExited(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunCancellationKillsOrdinaryChildProcess(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := run(ctx, "sh", "-c", `sleep 60 & child=$!; printf '%s\n' "$child" > "$1"; wait "$child"`, "agent-test", pidFile)
		done <- err
	}()
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("child process did not report its PID")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("child was not alive before cancellation: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled command reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after cancellation")
	}
	if !waitProcessExited(pid, 3*time.Second) {
		t.Fatalf("ordinary child process %d survived cancellation", pid)
	}
}
