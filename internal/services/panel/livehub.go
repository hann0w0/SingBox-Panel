package panel

import (
	"encoding/json"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

const defaultGlobalFlushInterval = time.Second

// liveEvent is one real-time sample delivered to SSE subscribers.
type liveEvent struct {
	Kind string      `json:"kind"` // "traffic" | "progress" | "log" | "server_status" | "server_status_batch"
	TS   int64       `json:"ts"`   // unix ms
	Data interface{} `json:"data"`
}

// serverLiveStatus is a partial live telemetry update for one server.
// Nil fields are omitted so traffic-only updates do not overwrite heartbeat
// metrics (load, memory, service state) and vice versa.
type serverLiveStatus struct {
	ServerID            uint     `json:"server_id"`
	Online              *bool    `json:"online,omitempty"`
	Load1               *float64 `json:"load1,omitempty"`
	MemUsed             *uint64  `json:"mem_used,omitempty"`
	MemTotal            *uint64  `json:"mem_total,omitempty"`
	Uptime              *int64   `json:"uptime,omitempty"`
	SingboxActive       *bool    `json:"singbox_active,omitempty"`
	TrafficAvailable    *bool    `json:"traffic_available,omitempty"`
	TrafficUploadRate   *uint64  `json:"traffic_upload_rate,omitempty"`
	TrafficDownloadRate *uint64  `json:"traffic_download_rate,omitempty"`
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
	mu                  sync.Mutex
	subs                map[uint][]*trafficSub    // serverID -> subscribers
	globalSubs          []*trafficSub             // subscribers receiving batched server_status_batch for all servers
	pendingGlobal       map[uint]serverLiveStatus // coalesced updates waiting for the next 1s global tick
	globalFlusherStop   chan struct{}
	globalFlushInterval time.Duration

	// recentTraffic keeps the last 120 samples (≈6 min at 3s interval) per server
	// so a newly connected SSE client gets immediate history instead of an empty chart.
	recentTraffic map[uint][]liveEvent
}

func newLiveHub() *liveHub {
	return &liveHub{
		subs:                make(map[uint][]*trafficSub),
		pendingGlobal:       make(map[uint]serverLiveStatus),
		globalFlushInterval: defaultGlobalFlushInterval,
		recentTraffic:       make(map[uint][]liveEvent),
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

func mergeServerLiveStatus(dst *serverLiveStatus, src serverLiveStatus) {
	dst.ServerID = src.ServerID
	if src.Online != nil {
		dst.Online = src.Online
	}
	if src.Load1 != nil {
		dst.Load1 = src.Load1
	}
	if src.MemUsed != nil {
		dst.MemUsed = src.MemUsed
	}
	if src.MemTotal != nil {
		dst.MemTotal = src.MemTotal
	}
	if src.Uptime != nil {
		dst.Uptime = src.Uptime
	}
	if src.SingboxActive != nil {
		dst.SingboxActive = src.SingboxActive
	}
	if src.TrafficAvailable != nil {
		dst.TrafficAvailable = src.TrafficAvailable
	}
	if src.TrafficUploadRate != nil {
		dst.TrafficUploadRate = src.TrafficUploadRate
	}
	if src.TrafficDownloadRate != nil {
		dst.TrafficDownloadRate = src.TrafficDownloadRate
	}
}

// subscribeAll registers an SSE subscriber that receives batched server_status_batch
// updates for every server on a steady 1s cadence (used by the admin servers list).
func (lh *liveHub) subscribeAll() *trafficSub {
	sub := &trafficSub{ch: make(chan liveEvent, 32)}
	lh.mu.Lock()
	lh.globalSubs = append(lh.globalSubs, sub)
	if lh.globalFlusherStop == nil {
		stop := make(chan struct{})
		lh.globalFlusherStop = stop
		interval := lh.globalFlushInterval
		if interval <= 0 {
			interval = defaultGlobalFlushInterval
		}
		go lh.runGlobalFlusher(stop, interval)
	}
	lh.mu.Unlock()
	return sub
}

func (lh *liveHub) unsubscribeAll(sub *trafficSub) {
	lh.mu.Lock()
	for i, s := range lh.globalSubs {
		if s == sub {
			lh.globalSubs = append(lh.globalSubs[:i], lh.globalSubs[i+1:]...)
			break
		}
	}
	if len(lh.globalSubs) == 0 {
		if lh.globalFlusherStop != nil {
			close(lh.globalFlusherStop)
			lh.globalFlusherStop = nil
		}
		clear(lh.pendingGlobal)
	}
	lh.mu.Unlock()
	close(sub.ch)
}

func (lh *liveHub) runGlobalFlusher(stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			lh.flushGlobal()
		}
	}
}

func (lh *liveHub) flushGlobal() {
	lh.mu.Lock()
	defer lh.mu.Unlock()
	if len(lh.globalSubs) == 0 || len(lh.pendingGlobal) == 0 {
		return
	}
	batch := make([]serverLiveStatus, 0, len(lh.pendingGlobal))
	for _, status := range lh.pendingGlobal {
		batch = append(batch, status)
	}
	clear(lh.pendingGlobal)
	sort.Slice(batch, func(i, j int) bool { return batch[i].ServerID < batch[j].ServerID })
	evt := liveEvent{
		Kind: "server_status_batch",
		TS:   time.Now().UnixMilli(),
		Data: batch,
	}
	for _, sub := range lh.globalSubs {
		select {
		case sub.ch <- evt:
		default:
		}
	}
}

// publishServerStatus broadcasts a partial server telemetry update immediately
// to per-server subscribers, and coalesces it for the next 1s global batch tick.
func (lh *liveHub) publishServerStatus(status serverLiveStatus) {
	evt := liveEvent{
		Kind: "server_status",
		TS:   time.Now().UnixMilli(),
		Data: status,
	}
	lh.mu.Lock()
	for _, sub := range lh.subs[status.ServerID] {
		select {
		case sub.ch <- evt:
		default:
		}
	}
	if len(lh.globalSubs) > 0 {
		cur := lh.pendingGlobal[status.ServerID]
		mergeServerLiveStatus(&cur, status)
		lh.pendingGlobal[status.ServerID] = cur
	}
	lh.mu.Unlock()
}

// publishTraffic stores a traffic sample in the rolling window and broadcasts
// it to all subscribers for that server.
func (lh *liveHub) publishTraffic(serverID uint, snapshot *protocol.TrafficSnapshot) {
	// A null sample marks the source unavailable. Keep it in the backlog so
	// reconnecting viewers cannot mistake the last good rate for a live one.
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
	delete(lh.pendingGlobal, serverID)
	lh.mu.Unlock()
}

// SSE formats a liveEvent as an SSE data frame.
func (e liveEvent) SSE() string {
	// Encode age at delivery time, not publication time, so backlog and queued
	// events can be expired without comparing the browser and server clocks.
	data, _ := json.Marshal(struct {
		liveEvent
		AgeMS int64 `json:"age_ms"`
	}{liveEvent: e, AgeMS: max(int64(0), time.Now().UnixMilli()-e.TS)})
	return "data: " + string(data) + "\n\n"
}
