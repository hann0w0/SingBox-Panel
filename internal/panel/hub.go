package panel

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/protocol"
)

// ErrAgentOffline is returned when a command targets a disconnected agent.
var ErrAgentOffline = errors.New("agent offline")

const (
	hubWriteWait  = 10 * time.Second
	hubPongWait   = 90 * time.Second
	hubPingPeriod = (hubPongWait * 9) / 10
)

// Hub tracks live agent connections and brokers request/response commands.
type Hub struct {
	db    *gorm.DB
	conns map[uint]*agentConn
	mu    sync.RWMutex

	// AfterRegister, if set, runs when an agent (re)registers — used to push the
	// latest config so a reconnecting server converges automatically.
	AfterRegister func(serverID uint)
}

// NewHub builds a Hub bound to a database.
func NewHub(db *gorm.DB) *Hub {
	return &Hub{db: db, conns: map[uint]*agentConn{}}
}

// agentConn is one live agent WebSocket connection.
type agentConn struct {
	serverID uint
	conn     *websocket.Conn
	send     chan protocol.Envelope
	hub      *Hub
	done     chan struct{}

	mu      sync.Mutex
	pending map[string]chan protocol.CommandResultEvt
	closed  bool
}

// register installs a connection for a server, displacing any previous one.
func (h *Hub) register(serverID uint, conn *websocket.Conn) *agentConn {
	ac := &agentConn{
		serverID: serverID,
		conn:     conn,
		send:     make(chan protocol.Envelope, 64),
		hub:      h,
		done:     make(chan struct{}),
		pending:  map[string]chan protocol.CommandResultEvt{},
	}
	h.mu.Lock()
	if old := h.conns[serverID]; old != nil {
		old.close()
	}
	h.conns[serverID] = ac
	h.mu.Unlock()
	touchServerSeen(h.db, serverID, true)
	return ac
}

func (h *Hub) unregister(ac *agentConn) {
	h.mu.Lock()
	if h.conns[ac.serverID] == ac {
		delete(h.conns, ac.serverID)
		// Keep the connection map decision and the persisted offline transition
		// under the same lock. Otherwise a replacement could register and write
		// online=true between unlock and this write, then be overwritten by the
		// old connection's delayed offline update.
		touchServerSeen(h.db, ac.serverID, false)
	}
	h.mu.Unlock()
}

// IsOnline reports whether a server's agent is connected.
func (h *Hub) IsOnline(serverID uint) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[serverID]
	return ok
}

// Disconnect closes a node connection after its database record has been
// deleted. A removed Agent cannot reconnect because its token no longer exists.
func (h *Hub) Disconnect(serverID uint) {
	h.mu.RLock()
	ac := h.conns[serverID]
	h.mu.RUnlock()
	if ac != nil {
		ac.close()
	}
}

// OnlineServerIDs returns the currently-connected server IDs.
func (h *Hub) OnlineServerIDs() []uint {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]uint, 0, len(h.conns))
	for id := range h.conns {
		out = append(out, id)
	}
	return out
}

// SendCommand sends a command to an agent and waits for its result. The caller
// controls the timeout via ctx.
func (h *Hub) SendCommand(ctx context.Context, serverID uint, t protocol.MessageType, payload any) (protocol.CommandResultEvt, error) {
	h.mu.RLock()
	ac := h.conns[serverID]
	h.mu.RUnlock()
	if ac == nil {
		return protocol.CommandResultEvt{}, ErrAgentOffline
	}

	id := uuid.NewString()
	env, err := protocol.NewEnvelope(t, id, payload)
	if err != nil {
		return protocol.CommandResultEvt{}, err
	}

	ch := make(chan protocol.CommandResultEvt, 1)
	ac.mu.Lock()
	ac.pending[id] = ch
	ac.mu.Unlock()
	defer func() {
		ac.mu.Lock()
		delete(ac.pending, id)
		ac.mu.Unlock()
	}()

	select {
	case ac.send <- env:
	case <-ac.done:
		return protocol.CommandResultEvt{}, ErrAgentOffline
	case <-ctx.Done():
		return protocol.CommandResultEvt{}, ctx.Err()
	}

	select {
	case res := <-ch:
		if !res.OK {
			return res, errors.New(res.Error)
		}
		return res, nil
	case <-ac.done:
		return protocol.CommandResultEvt{}, ErrAgentOffline
	case <-ctx.Done():
		return protocol.CommandResultEvt{}, ctx.Err()
	}
}

// serve runs the read and write pumps until the connection ends.
func (ac *agentConn) serve() {
	go ac.writePump()
	ac.readPump()
}

func (ac *agentConn) close() {
	ac.mu.Lock()
	if ac.closed {
		ac.mu.Unlock()
		return
	}
	ac.closed = true
	close(ac.done)
	ac.mu.Unlock()
	_ = ac.conn.Close()
}

func (ac *agentConn) readPump() {
	defer func() {
		ac.close()
		ac.hub.unregister(ac)
	}()
	_ = ac.conn.SetReadDeadline(time.Now().Add(hubPongWait))
	ac.conn.SetPongHandler(func(string) error {
		return ac.conn.SetReadDeadline(time.Now().Add(hubPongWait))
	})
	for {
		_, data, err := ac.conn.ReadMessage()
		if err != nil {
			return
		}
		_ = ac.conn.SetReadDeadline(time.Now().Add(hubPongWait))
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		if env.Type == protocol.EvtCommandResult {
			ac.routeResult(env)
			continue
		}
		ac.hub.handleEvent(ac.serverID, env)
	}
}

func (ac *agentConn) writePump() {
	ping := time.NewTicker(hubPingPeriod)
	defer ping.Stop()
	for {
		select {
		case <-ac.done:
			return
		case env := <-ac.send:
			_ = ac.conn.SetWriteDeadline(time.Now().Add(hubWriteWait))
			if err := ac.conn.WriteJSON(env); err != nil {
				ac.close()
				return
			}
		case <-ping.C:
			_ = ac.conn.SetWriteDeadline(time.Now().Add(hubWriteWait))
			if err := ac.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				ac.close()
				return
			}
		}
	}
}

func (ac *agentConn) routeResult(env protocol.Envelope) {
	var res protocol.CommandResultEvt
	if err := env.Decode(&res); err != nil {
		return
	}
	ac.mu.Lock()
	ch := ac.pending[env.ID]
	ac.mu.Unlock()
	if ch != nil {
		select {
		case ch <- res:
		default:
		}
	}
}

// handleEvent persists a spontaneous agent event.
func (h *Hub) handleEvent(serverID uint, env protocol.Envelope) {
	switch env.Type {
	case protocol.EvtRegister:
		var e protocol.RegisterEvt
		if env.Decode(&e) == nil {
			h.onRegister(serverID, e)
		}
	case protocol.EvtHeartbeat:
		var e protocol.HeartbeatEvt
		if env.Decode(&e) == nil {
			h.onHeartbeat(serverID, e)
		}
	case protocol.EvtLog:
		var e protocol.LogEvt
		if env.Decode(&e) == nil {
			log.Printf("agent[%d] %s: %s", serverID, e.Level, e.Msg)
		}
	}
}

func (h *Hub) onRegister(serverID uint, e protocol.RegisterEvt) {
	now := time.Now()
	updates := map[string]any{
		"online":            true,
		"last_seen":         &now,
		"hostname":          e.Hostname,
		"os":                e.OS,
		"arch":              e.Arch,
		"kernel":            e.Kernel,
		"agent_version":     e.AgentVersion,
		"singbox_installed": e.SingboxInstalled,
		"singbox_version":   e.SingboxVersion,
		"agent_url":         e.PanelURL,
	}
	if ip, ok := normalizedIPv4(e.PublicIP); ok {
		updates["public_ip"] = ip
	}
	h.db.Model(&model.Server{}).Where("id = ?", serverID).Updates(updates)
	if h.AfterRegister != nil {
		h.AfterRegister(serverID)
	}
}

func (h *Hub) onHeartbeat(serverID uint, e protocol.HeartbeatEvt) {
	now := time.Now()
	updates := map[string]any{
		"online":         true,
		"last_seen":      &now,
		"load1":          e.Load1,
		"mem_used":       e.MemUsed,
		"mem_total":      e.MemTotal,
		"uptime":         e.Uptime,
		"singbox_active": e.SingboxActive,
	}
	if e.SingboxVersion != "" {
		updates["singbox_version"] = e.SingboxVersion
		updates["singbox_installed"] = true
	}
	h.db.Model(&model.Server{}).Where("id = ?", serverID).Updates(updates)
	if err := recordServerTraffic(h.db, serverID, e.Traffic); err != nil {
		log.Printf("agent[%d] record traffic: %v", serverID, err)
	}
}
