// Package agent implements the VPS-side daemon: it installs and manages the
// official sing-box, applies panel-generated configs, and reverse-connects to
// the panel over WebSocket. It never runs arbitrary shell from the panel; only
// the fixed command set is handled.
package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maxCommandOutputBytes = 1 << 20

type cappedCommandOutput struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
}

func (b *cappedCommandOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)
	remaining := maxCommandOutputBytes - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
			b.truncated = true
		}
		_, _ = b.buf.Write(p)
	} else {
		b.truncated = true
	}
	return written, nil
}

func (b *cappedCommandOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.buf.String()
	if b.truncated {
		out += "\n[command output truncated]"
	}
	return out
}

// run executes a command and returns trimmed combined output.
func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	var buf cappedCommandOutput
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

// runShell executes a /bin/sh -c script (used for the official install pipes).
func runShell(ctx context.Context, script string) (string, error) {
	return run(ctx, "sh", "-c", script)
}
