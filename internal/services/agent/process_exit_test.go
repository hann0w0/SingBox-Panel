package agent

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func waitForTestProcessExit(timeout time.Duration, readState func() (string, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		state, err := readState()
		if errors.Is(err, os.ErrNotExist) || (err == nil && (state == "Z" || state == "X")) {
			// Exit is terminal. A second probe can race with reaping or PID
			// reuse and must never overturn this successful observation.
			return nil
		}
		if err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("process still alive (state %s)", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProcessExitWaitDoesNotRecheckReapedZombie(t *testing.T) {
	reads := 0
	err := waitForTestProcessExit(time.Second, func() (string, error) {
		reads++
		if reads == 1 {
			return "Z", nil
		}
		// The process may be reaped between a successful zombie observation
		// and a second read. Once exited, its PID can also be reused.
		return "", os.ErrNotExist
	})
	if err != nil || reads != 1 {
		t.Fatalf("reaped zombie reported alive: reads=%d error=%v", reads, err)
	}
}

func TestProcessExitWaitAcceptsMissingProcess(t *testing.T) {
	if err := waitForTestProcessExit(time.Second, func() (string, error) {
		return "", &os.PathError{Op: "open", Path: "/proc/123/stat", Err: os.ErrNotExist}
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProcessExitWaitRejectsLiveOrUnreadableProcess(t *testing.T) {
	for _, test := range []struct {
		name  string
		state string
		err   error
	}{
		{name: "running", state: "R"},
		{name: "sleeping", state: "S"},
		{name: "permission", err: os.ErrPermission},
		{name: "malformed", err: errors.New("invalid process stat format")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := waitForTestProcessExit(0, func() (string, error) { return test.state, test.err }); err == nil {
				t.Fatal("live or unreadable process reported exited")
			}
		})
	}
}
