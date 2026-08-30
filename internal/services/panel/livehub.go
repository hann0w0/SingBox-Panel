package panel

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

// liveEvent is one real-time sample delivered to SSE subscribers.
type liveEvent struct {
	Kind string      `json:"kind"` // "traffic" | "progress" | "log"
	TS   int64       `json:"ts"`   // unix ms
	Data interface{} `json:"data"`
}

// trafficSub is one SSE subscriber for a specific server.
type trafficSub struct {
	ch chan liveEvent
}

// liveHub maintains in-memory rolling windows of recent traffic samples per
// server and broadcasts new events to SSE subscribers. It is decoupled from the
// WebSocket Hub so SSE delivery never blocks agent command processing.
//
// Delivery invariants: unsubscribe removes the subscriber from lh.subs and only
// then closes its channel, while every publish sends while holding lh.mu. Sends
// are therefore serialized against removal+close, so a publisher can never send
// on a closed channel (which would panic) — a subscriber that is too slow to
// drain its buffer simply drops the sample.
type liveHub struct {
	mu   sync.Mutex
	subs map[uint][]*trafficSub // serverID -> subscribers

	// recentTraffic keeps the last 120 samples (≈6 min at 3s interval) per server
	// so a newly connected SSE client gets immediate history instead of an empty chart.
	recentTraffic map[uint][]liveEvent
}

func newLiveHub() *liveHub {
	return &liveHub{
		subs:          make(map[uint][]*trafficSub),
		recentTraffic: make(map[uint][]liveEvent),
	}
}

// subscribe registers an SSE subscriber for a server and returns the subscription
// plus a backlog of recent samples.
func (lh *liveHub) subscribe(serverID uint) (*trafficSub, []liveEvent) {
	sub := &trafficSub{ch: make(chan liveEvent, 64)}
	lh.mu.Lock()
	lh.subs[serverID] = append(lh.subs[serverID], sub)
	recent := lh.recentTraffic[serverID]
	backlog := make([]liveEvent, len(recent))
	copy(backlog, recent)
	lh.mu.Unlock()
	return sub, backlog
}

func (lh *liveHub) unsubscribe(serverID uint, sub *trafficSub) {
	lh.mu.Lock()
	subs := lh.subs[serverID]
	for i, s := range subs {
		if s == sub {
			lh.subs[serverID] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	if len(lh.subs[serverID]) == 0 {
		delete(lh.subs, serverID)
	}
	lh.mu.Unlock()
	close(sub.ch)
}

// publishTraffic stores a traffic sample in the rolling window and broadcasts
// it to all subscribers for that server.
func (lh *liveHub) publishTraffic(serverID uint, snapshot *protocol.TrafficSnapshot) {
	if snapshot == nil {
		return
	}
	evt := liveEvent{
		Kind: "traffic",
		TS:   time.Now().UnixMilli(),
		Data: snapshot,
	}
	lh.mu.Lock()
	rt := lh.recentTraffic[serverID]
	rt = append(rt, evt)
	if len(rt) > 120 {
		rt = rt[len(rt)-120:]
	}
	lh.recentTraffic[serverID] = rt
	for _, sub := range lh.subs[serverID] {
		select {
		case sub.ch <- evt:
		default: // drop if subscriber is slow
		}
	}
	lh.mu.Unlock()
}

// publishProgress forwards a progress event to subscribers (and logs it).
func (lh *liveHub) publishProgress(serverID uint, evt protocol.ProgressEvt) {
	le := liveEvent{
		Kind: "progress",
		TS:   time.Now().UnixMilli(),
		Data: evt,
	}
	log.Printf("agent[%d] progress[%s]: %s", serverID, evt.ID, evt.Line)
	lh.mu.Lock()
	for _, sub := range lh.subs[serverID] {
		select {
		case sub.ch <- le:
		default:
		}
	}
	lh.mu.Unlock()
}

// publishLog forwards a log line to subscribers.
func (lh *liveHub) publishLog(serverID uint, evt protocol.LogEvt) {
	le := liveEvent{
		Kind: "log",
		TS:   time.Now().UnixMilli(),
		Data: evt,
	}
	lh.mu.Lock()
	for _, sub := range lh.subs[serverID] {
		select {
		case sub.ch <- le:
		default:
		}
	}
	lh.mu.Unlock()
}

func (lh *liveHub) clearServer(serverID uint) {
	lh.mu.Lock()
	delete(lh.recentTraffic, serverID)
	lh.mu.Unlock()
}

// SSE formats a liveEvent as an SSE data frame.
func (e liveEvent) SSE() string {
	data, _ := json.Marshal(e)
	return "data: " + string(data) + "\n\n"
}
