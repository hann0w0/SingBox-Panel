package agent

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"strconv"
	"sync"
)

// logStreamer manages a continuous journalctl -u sing-box -f subprocess per
// agent session. The panel starts/stops it via CmdStreamLogs; each line is
// pushed as an EvtLog event so the UI can display a live tail without polling.
type logStreamer struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
}

func newLogStreamer() *logStreamer {
	return &logStreamer{}
}

// start begins streaming sing-box logs. If already running, the previous stream
// is replaced. Each line is delivered to the provided callback in real time.
// The stream runs independently of the caller's context (which may have a
// short command timeout) and is stopped explicitly via stop() or on program exit.
func (ls *logStreamer) start(lines int, onLine func(string)) error {
	ls.stop()

	if lines <= 0 {
		lines = 200
	}
	if lines > maxLogLines {
		lines = maxLogLines
	}

	streamCtx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(streamCtx, "journalctl", "-u", ServiceName,
		"-n", strconv.Itoa(lines),
		"-f", "--no-pager")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}

	ls.mu.Lock()
	ls.cancel = cancel
	ls.running = true
	ls.mu.Unlock()

	go func() {
		defer func() {
			ls.mu.Lock()
			ls.running = false
			ls.cancel = nil
			ls.mu.Unlock()
		}()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)
		for scanner.Scan() {
			select {
			case <-streamCtx.Done():
				return
			default:
			}
			onLine(scanner.Text())
		}
		_, _ = io.Copy(io.Discard, stdout)
		_ = cmd.Wait()
	}()

	return nil
}

// stop terminates the active log stream, if any.
func (ls *logStreamer) stop() {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.cancel != nil {
		ls.cancel()
		ls.cancel = nil
	}
	ls.running = false
}

