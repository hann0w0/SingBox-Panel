// Package agent implements the VPS-side daemon: it installs and manages the
// official sing-box, applies panel-generated configs, and reverse-connects to
// the panel over WebSocket. It never runs arbitrary shell from the panel; only
// the fixed command set is handled.
package agent

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
)

// run executes a command and returns trimmed combined output.
func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

// runShell executes a /bin/sh -c script (used for the official install pipes).
func runShell(ctx context.Context, script string) (string, error) {
	return run(ctx, "sh", "-c", script)
}
