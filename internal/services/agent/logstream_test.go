package agent

import (
	"context"
	"testing"
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
