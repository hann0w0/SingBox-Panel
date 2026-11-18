package agent

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maxStreamLogLineBytes = 256 * 1024

// logStreamer manages one continuous native log follower per Agent session. The
// panel starts/stops it via CmdStreamLogs; each line is pushed as an EvtLog
// event so the UI can display a live tail without polling.
type logStreamer struct {
	operationMu sync.Mutex
	mu          sync.Mutex
	cancel      context.CancelFunc
	running     bool
	generation  uint64
	done        chan struct{}
	command     func(context.Context, int) (*exec.Cmd, error)
}

func newLogStreamer() *logStreamer {
	return &logStreamer{command: logStreamCommand}
}

func logStreamCommand(ctx context.Context, lines int) (*exec.Cmd, error) {
	manager, err := currentServiceManager()
	if err != nil {
		return nil, err
	}
	command, args := manager.LogCommand(lines, true)
	return exec.CommandContext(ctx, command, args...), nil
}

// start begins streaming sing-box logs. If already running, the previous stream
// is replaced. Each line is delivered to the provided callback in real time.
// The stream runs independently of the caller's context (which may have a
// short command timeout) and is stopped explicitly via stop() or on program exit.
func (ls *logStreamer) start(lines int, onLine func(string)) error {
	ls.operationMu.Lock()
	defer ls.operationMu.Unlock()

	ls.stopLocked()

	if lines <= 0 {
		lines = 200
	}
	if lines > maxLogLines {
		lines = maxLogLines
	}

	streamCtx, cancel := context.WithCancel(context.Background())

	follower, err := ls.startFollower(streamCtx, lines)
	if err != nil {
		cancel()
		return err
	}

	done := make(chan struct{})
	ls.mu.Lock()
	generation := ls.generation
	ls.cancel = cancel
	ls.running = true
	ls.done = done
	ls.mu.Unlock()

	go func() {
		defer close(done)
		defer ls.finish(generation)
		defer cancel()
		ls.follow(streamCtx, follower, onLine)
	}()
	return nil
}

type logFollower struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	cancel context.CancelFunc
}

func (ls *logStreamer) startFollower(ctx context.Context, lines int) (*logFollower, error) {
	followerCtx, cancel := context.WithCancel(ctx)
	cmd, err := ls.command(followerCtx, lines)
	if err != nil {
		cancel()
		return nil, err
	}
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
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		_ = stdout.Close()
		return nil, err
	}
	return &logFollower{cmd: cmd, stdout: stdout, cancel: cancel}, nil
}

func (ls *logStreamer) follow(ctx context.Context, follower *logFollower, onLine func(string)) {
	backoff := minReconnectBackoff
	for {
		started := time.Now()
		stdout := follower.stdout
		closePipe := context.AfterFunc(ctx, func() { _ = stdout.Close() })
		readErr := readStreamLogLines(ctx, follower.stdout, onLine)
		closePipe()
		follower.cancel()
		waitErr := follower.cmd.Wait()
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= stableConnectionTime {
			backoff = minReconnectBackoff
		}
		err := waitErr
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			err = readErr
		}
		if err == nil {
			err = io.EOF
		}
		for {
			log.Printf("agent: log follower stopped: %v; retry in %s", err, backoff)
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			backoff = followingReconnectBackoff(backoff, 0)
			if ctx.Err() != nil {
				return
			}
			follower, err = ls.startFollower(ctx, 0)
			if err == nil {
				break
			}
		}
	}
}

func readStreamLogLines(ctx context.Context, source io.Reader, onLine func(string)) error {
	reader := bufio.NewReaderSize(source, 64*1024)
	var line []byte
	truncated := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if ctx.Err() != nil {
			return ctx.Err()
		}
		remaining := maxStreamLogLineBytes - len(line)
		line = append(line, fragment[:min(len(fragment), remaining)]...)
		truncated = truncated || len(fragment) > remaining
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if len(line) > 0 {
			if truncated {
				onLine(string(line) + " [log line truncated]")
			} else {
				onLine(strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r"))
			}
		}
		line = line[:0]
		truncated = false
		if err != nil {
			return err
		}
	}
}

// finish only clears state for the stream that is still current. A replaced
// follower goroutine can exit after its successor has started, and must not
// mark that newer stream as stopped.
func (ls *logStreamer) finish(generation uint64) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.generation != generation {
		return
	}
	ls.running = false
	ls.cancel = nil
}

// stop terminates the active log stream, if any.
func (ls *logStreamer) stop() {
	ls.operationMu.Lock()
	defer ls.operationMu.Unlock()
	ls.stopLocked()
}

func (ls *logStreamer) stopLocked() {
	ls.mu.Lock()
	ls.generation++
	cancel := ls.cancel
	done := ls.done
	ls.cancel = nil
	ls.done = nil
	ls.running = false
	ls.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
