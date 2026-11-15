package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestLogStreamerOldGenerationCannotClearCurrentStream(t *testing.T) {
	ls := newLogStreamer()
	_, cancel := context.WithCancel(context.Background())
	ls.generation = 2
	ls.running = true
	ls.cancel = cancel

	ls.finish(1)

	if !ls.running || ls.cancel == nil {
		t.Fatal("old stream cleanup cleared the current stream")
	}
	ls.stop()
}

func TestLogStreamerCurrentGenerationClearsState(t *testing.T) {
	ls := newLogStreamer()
	_, cancel := context.WithCancel(context.Background())
	ls.generation = 3
	ls.running = true
	ls.cancel = cancel

	ls.finish(3)

	if ls.running || ls.cancel != nil {
		t.Fatal("current stream cleanup did not clear state")
	}
}

func TestReadStreamLogLinesTruncatesOversizedLineAndContinues(t *testing.T) {
	var lines []string
	raw := "before\r\n" + strings.Repeat("x", 3*maxStreamLogLineBytes) + "\nafter\nfinal"
	err := readStreamLogLines(context.Background(), strings.NewReader(raw), func(line string) {
		lines = append(lines, line)
	})
	if !errors.Is(err, io.EOF) || len(lines) != 4 {
		t.Fatalf("lines=%d error=%v", len(lines), err)
	}
	if lines[0] != "before" || lines[2] != "after" || lines[3] != "final" {
		t.Fatalf("normal lines changed: %q / %q / %q", lines[0], lines[2], lines[3])
	}
	if lines[1] != strings.Repeat("x", maxStreamLogLineBytes)+" [log line truncated]" {
		t.Fatalf("oversized line has unexpected length %d", len(lines[1]))
	}
}

func TestLogStreamerRetriesExitAndStartFailuresWithoutNewCommand(t *testing.T) {
	ls := newLogStreamer()
	var commands []*exec.Cmd
	var requestedLines []int
	var mu sync.Mutex
	ls.command = func(ctx context.Context, lines int) (*exec.Cmd, error) {
		mu.Lock()
		defer mu.Unlock()
		requestedLines = append(requestedLines, lines)
		if len(requestedLines) == 2 {
			return exec.CommandContext(ctx, "/missing/agent-log-follower"), nil
		}
		mode := "exit"
		if len(requestedLines) > 2 {
			mode = "wait"
		}
		cmd := logFollowerTestCommand(ctx, mode)
		commands = append(commands, cmd)
		return cmd, nil
	}
	lines := make(chan string, 4)
	if err := ls.start(123, func(line string) { lines <- line }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ls.stop)
	for _, want := range []string{"exit", "wait"} {
		select {
		case line := <-lines:
			if line != want {
				t.Fatalf("line=%q, want %q", line, want)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("follower did not recover from process exit and failed restart")
		}
	}
	ls.stop()
	mu.Lock()
	defer mu.Unlock()
	if len(requestedLines) != 3 || requestedLines[0] != 123 || requestedLines[1] != 0 || requestedLines[2] != 0 {
		t.Fatalf("initial/retry tails = %v", requestedLines)
	}
	for _, cmd := range commands {
		if cmd.ProcessState == nil {
			t.Fatal("stop returned without reaping a follower")
		}
	}
}

func TestLogStreamerStopCancelsBackoff(t *testing.T) {
	ls := newLogStreamer()
	var attempts int
	var mu sync.Mutex
	var first *exec.Cmd
	ls.command = func(ctx context.Context, _ int) (*exec.Cmd, error) {
		mu.Lock()
		attempts++
		cmd := logFollowerTestCommand(ctx, "exit")
		if first == nil {
			first = cmd
		}
		mu.Unlock()
		return cmd, nil
	}
	lines := make(chan string, 1)
	if err := ls.start(1, func(line string) { lines <- line }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ls.stop)
	select {
	case <-lines:
	case <-time.After(3 * time.Second):
		t.Fatal("follower did not start")
	}
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(first.Process.Pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if syscall.Kill(first.Process.Pid, 0) == nil {
		t.Fatal("follower did not exit before the backoff cancellation test")
	}
	started := time.Now()
	ls.stop()
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("stop waited for restart backoff")
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 1 {
		t.Fatalf("stopped session launched %d followers", attempts)
	}
}

func TestLogStreamerReplacementAndStopReapProcessGroup(t *testing.T) {
	ls := newLogStreamer()
	var commands []*exec.Cmd
	ls.command = func(ctx context.Context, _ int) (*exec.Cmd, error) {
		cmd := logFollowerTestCommand(ctx, "child")
		commands = append(commands, cmd)
		return cmd, nil
	}
	childPIDs := make(chan string, 2)
	var pids []int
	t.Cleanup(func() {
		ls.stop()
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	for replacement := 0; replacement < 2; replacement++ {
		if err := ls.start(1, func(line string) { childPIDs <- line }); err != nil {
			t.Fatal(err)
		}
		if replacement > 0 && commands[replacement-1].ProcessState == nil {
			t.Fatal("replacement did not reap previous follower")
		}
		select {
		case value := <-childPIDs:
			pid, err := strconv.Atoi(value)
			if err != nil {
				t.Fatal(err)
			}
			pids = append(pids, pid)
		case <-time.After(3 * time.Second):
			t.Fatal("follower child did not start")
		}
	}
	ls.stop()
	if commands[1].ProcessState == nil {
		t.Fatal("stop did not reap current follower")
	}
	for _, pid := range pids {
		deadline := time.Now().Add(3 * time.Second)
		for syscall.Kill(pid, 0) == nil && !processIsZombieAt("/proc", pid) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if err := syscall.Kill(pid, 0); err == nil && !processIsZombieAt("/proc", pid) {
			t.Fatalf("follower descendant %d survived stop", pid)
		}
	}
}

func logFollowerTestCommand(ctx context.Context, mode string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLogFollowerProcess$", "--", mode)
	cmd.Env = append(os.Environ(), "SINGBOX_PANEL_TEST_LOG_FOLLOWER=1")
	return cmd
}

func TestLogFollowerProcess(t *testing.T) {
	if os.Getenv("SINGBOX_PANEL_TEST_LOG_FOLLOWER") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	if mode == "child" {
		cmd := logFollowerTestCommand(context.Background(), "wait")
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Println(cmd.Process.Pid)
		_ = cmd.Wait()
		os.Exit(0)
	}
	fmt.Println(mode)
	if mode == "wait" {
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(1)
}
