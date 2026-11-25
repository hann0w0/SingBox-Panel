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

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

// ErrAgentOffline is returned when a command targets a disconnected agent.
var ErrAgentOffline = errors.New("agent offline")

const (
	hubWriteWait             = 10 * time.Second
	hubPongWait              = 90 * time.Second
	hubPingPeriod            = (hubPongWait * 9) / 10
	maxPanelAgentMessageSize = 16 << 20
	maxPanelAgentPayloadSize = 15 << 20
)

// Hub tracks live agent connections and brokers request/response commands.
type Hub struct {
	db    *gorm.DB
	conns map[uint]*agentConn
	mu    sync.RWMutex
	live  *liveHub
	state *keyedMutex[uint]
	// traffic buffers per-minute history and writes it in batches.
	traffic *trafficWriter

	// AfterRegister, if set, runs when an agent (re)registers — used to push the
	// latest config so a reconnecting server converges automatically.
	AfterRegister func(serverID uint)
	// AfterStatsCapabilityChange, if set, runs when a heartbeat reports a
	// sing-box version that crosses the stats API threshold (e.g. after an
	// in-place upgrade to 1.14), so the config can enable or drop the service.
	AfterStatsCapabilityChange func(serverID uint)
	AfterAddressChange         func(serverID uint)
}

// NewHub builds a Hub bound to a database.
func NewHub(db *gorm.DB) *Hub {
	return &Hub{db: db, conns: map[uint]*agentConn{}, live: newLiveHub(), state: newKeyedMutex[uint](), traffic: newTrafficWriter(db)}
}

// agentConn is one live agent WebSocket connection.
type agentConn struct {
	serverID uint
	remoteIP string
	conn     *websocket.Conn
	send     chan protocol.Envelope
	hub      *Hub
	done     chan struct{}
	stopped  chan struct{}

	mu                           sync.Mutex
	pending                      map[string]chan protocol.CommandResultEvt
	closed                       bool
	restrictedOutboundValidation bool
	initialRegister              *protocol.RegisterEvt
}

// register installs a connection for a server, displacing any previous one.
func (h *Hub) register(serverID uint, remoteIP string, conn *websocket.Conn) *agentConn {
	return h.registerWithEvent(serverID, remoteIP, conn, nil)
}

func (h *Hub) registerWithEvent(serverID uint, remoteIP string, conn *websocket.Conn, reg *protocol.RegisterEvt) *agentConn {
	var initialCopy *protocol.RegisterEvt
	restrictedValidation := false
	if reg != nil {
		copied := *reg
		initialCopy = &copied
		restrictedValidation = reg.RestrictedOutboundValidation
	}
	ac := &agentConn{
		serverID:                     serverID,
		remoteIP:                     remoteIP,
		conn:                         conn,
		send:                         make(chan protocol.Envelope, 64),
		hub:                          h,
		done:                         make(chan struct{}),
		stopped:                      make(chan struct{}),
		pending:                      map[string]chan protocol.CommandResultEvt{},
		restrictedOutboundValidation: restrictedValidation,
		initialRegister:              initialCopy,
	}
	unlockState := h.state.lock(serverID)
	h.mu.Lock()
	old := h.conns[serverID]
	h.conns[serverID] = ac
	h.mu.Unlock()
	touchServerSeen(h.db, serverID, true)
	unlockState()
	online := true
	h.live.publishServerStatus(serverLiveStatus{
		ServerID: serverID,
		Online:   &online,
	})
	if old != nil {
		old.close()
	}
	return ac
}

func (h *Hub) connection(serverID uint) *agentConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.conns[serverID]
}

func (h *Hub) isCurrentConnection(serverID uint, ac *agentConn) bool {
	if ac == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.conns[serverID] == ac
}

func (h *Hub) unregister(ac *agentConn) {
	unlockState := h.state.lock(ac.serverID)
	defer unlockState()
	removed := false
	h.mu.Lock()
	if h.conns[ac.serverID] == ac {
		delete(h.conns, ac.serverID)
		removed = true
	}
	h.mu.Unlock()
	if removed {
		touchServerSeen(h.db, ac.serverID, false)
		if err := h.traffic.record(ac.serverID, nil); err != nil {
			log.Printf("traffic: clear offline server %d: %v", ac.serverID, err)
		}
		h.live.publishTraffic(ac.serverID, nil)
		offline := false
		zero := uint64(0)
		h.live.publishServerStatus(serverLiveStatus{
			ServerID:            ac.serverID,
			Online:              &offline,
			TrafficAvailable:    &offline,
			TrafficUploadRate:   &zero,
			TrafficDownloadRate: &zero,
		})
	}
}

// IsOnline reports whether a server's agent is connected.
func (h *Hub) IsOnline(serverID uint) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[serverID]
	return ok
}

// Close terminates all active agent WebSocket connections during server shutdown.
func (h *Hub) Close() {
	h.mu.RLock()
	conns := make([]*agentConn, 0, len(h.conns))
	for _, ac := range h.conns {
		if ac != nil {
			conns = append(conns, ac)
		}
	}
	h.mu.RUnlock()
	for _, ac := range conns {
		ac.close()
	}
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

// DisconnectAndWait closes the current connection and waits until its read
// pump has stopped. Deletion uses this before its final cleanup so an event
// already being persisted cannot recreate an orphan traffic row afterward.
func (h *Hub) DisconnectAndWait(ctx context.Context, serverID uint) error {
	h.mu.RLock()
	ac := h.conns[serverID]
	h.mu.RUnlock()
	if ac == nil {
		return nil
	}
	ac.close()
	if ac.stopped == nil {
		return nil
	}
	select {
	case <-ac.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
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
	ac := h.connection(serverID)
	if ac == nil {
		return protocol.CommandResultEvt{}, ErrAgentOffline
	}
	return h.sendCommandToConnection(ctx, ac, t, payload)
}

func (h *Hub) sendCommandToConnection(ctx context.Context, ac *agentConn, t protocol.MessageType, payload any) (protocol.CommandResultEvt, error) {
	if ac == nil || !h.isCurrentConnection(ac.serverID, ac) {
		return protocol.CommandResultEvt{}, ErrAgentOffline
	}
	if command, ok := payload.(protocol.ApplyConfigCmd); ok && len(command.RestrictedOutboundTags) > 0 {
		ac.mu.Lock()
		supported := ac.restrictedOutboundValidation
		ac.mu.Unlock()
		if !supported {
			return protocol.CommandResultEvt{}, errors.New("该节点的 Agent 需先升级，才能安全应用用户专属出站")
		}
	}

	id := uuid.NewString()
	env, err := protocol.NewEnvelope(t, id, payload)
	if err != nil {
		return protocol.CommandResultEvt{}, err
	}
	if encoded, err := json.Marshal(env); err != nil {
		return protocol.CommandResultEvt{}, err
	} else if len(encoded) > maxPanelAgentMessageSize {
		return protocol.CommandResultEvt{}, errors.New("下发命令体积超过 Agent 消息上限（16MB），请精简配置或规则后再试")
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
	if ac.stopped != nil {
		defer close(ac.stopped)
	}
	go ac.writePump()
	if ac.initialRegister != nil {
		reg := *ac.initialRegister
		ac.initialRegister = nil
		ac.hub.onRegister(ac, reg)
		ac.readPumpRegistered(true)
		return
	}
	ac.readPumpRegistered(false)
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
	if ac.conn != nil {
		_ = ac.conn.Close()
	}
}

func (ac *agentConn) readPump() {
	ac.readPumpRegistered(false)
}

func (ac *agentConn) readPumpRegistered(registered bool) {
	defer func() {
		ac.close()
		ac.hub.unregister(ac)
	}()
	ac.conn.SetReadLimit(maxPanelAgentMessageSize)
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
		if len(env.Payload) > maxPanelAgentPayloadSize {
			return
		}
		if !registered {
			if env.Type != protocol.EvtRegister {
				return
			}
			var event protocol.RegisterEvt
			if err := env.Decode(&event); err != nil {
				return
			}
			registered = true
			ac.hub.onRegister(ac, event)
			continue
		}
		if env.Type == protocol.EvtRegister {
			// Registration defines the connection identity and may trigger a config
			// push. It is valid exactly once as the first application message.
			return
		}
		if env.Type == protocol.EvtCommandResult {
			ac.routeResult(env)
			continue
		}
		ac.hub.handleEvent(ac, env)
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
func (h *Hub) handleEvent(ac *agentConn, env protocol.Envelope) {
	unlock := h.state.lock(ac.serverID)
	defer unlock()
	if !h.isCurrentConnection(ac.serverID, ac) {
		return
	}
	serverID := ac.serverID
	switch env.Type {
	case protocol.EvtHeartbeat:
		var e protocol.HeartbeatEvt
		if env.Decode(&e) == nil {
			h.onHeartbeat(ac, e)
		}
	case protocol.EvtTraffic:
		var e protocol.TrafficEvt
		if env.Decode(&e) == nil && e.Traffic != nil {
			recordErr := h.traffic.record(serverID, e.Traffic)
			if recordErr != nil && !errors.Is(recordErr, errFutureTrafficBucket) {
				log.Printf("traffic: record server %d: %v", serverID, recordErr)
				return
			}
			if recordErr != nil {
				log.Printf("traffic: record server %d: %v", serverID, recordErr)
			}
			if report := e.Traffic.Report; e.Traffic.AccountingVersion >= 2 && report != nil {
				ackSeq := report.Sequence
				if recordErr != nil {
					ackSeq = 0
				}
				ack, err := protocol.NewEnvelope(protocol.EvtTrafficAck, "", protocol.TrafficAck{
					Session:    report.Session,
					Sequence:   ackSeq,
					ServerTime: time.Now().UTC().Unix(),
				})
				if err == nil {
					select {
					case ac.send <- ack:
					default:
					} // the Agent will retry an unacknowledged batch
				}
			}
			// The live stream only needs node and port data; per-user deltas
			// stay out of its in-memory backlog.
			live := *e.Traffic
			live.Users = nil
			live.Report = nil
			if live.Unavailable {
				h.live.publishTraffic(serverID, nil)
				online := true
				available := false
				zero := uint64(0)
				h.live.publishServerStatus(serverLiveStatus{
					ServerID:            serverID,
					Online:              &online,
					TrafficAvailable:    &available,
					TrafficUploadRate:   &zero,
					TrafficDownloadRate: &zero,
				})
			} else {
				h.live.publishTraffic(serverID, &live)
				online := true
				available := true
				up := live.UploadRate
				down := live.DownloadRate
				h.live.publishServerStatus(serverLiveStatus{
					ServerID:            serverID,
					Online:              &online,
					TrafficAvailable:    &available,
					TrafficUploadRate:   &up,
					TrafficDownloadRate: &down,
				})
			}
		}
	case protocol.EvtMetrics:
		var e protocol.MetricsEvt
		if env.Decode(&e) == nil {
			online := true
			load1 := e.Load1
			memUsed := e.MemUsed
			memTotal := e.MemTotal
			available := e.TrafficAvailable
			up := e.UploadRate
			down := e.DownloadRate
			if !available {
				up, down = 0, 0
			}
			h.live.publishServerStatus(serverLiveStatus{
				ServerID:            serverID,
				Online:              &online,
				Load1:               &load1,
				MemUsed:             &memUsed,
				MemTotal:            &memTotal,
				TrafficAvailable:    &available,
				TrafficUploadRate:   &up,
				TrafficDownloadRate: &down,
			})
		}
	case protocol.EvtProgress:
		var e protocol.ProgressEvt
		if env.Decode(&e) == nil {
			h.live.publishProgress(serverID, e)
		}
	case protocol.EvtLog:
		var e protocol.LogEvt
		if env.Decode(&e) == nil {
			h.live.publishLog(serverID, e)
		}
	}
}

func (h *Hub) onRegister(ac *agentConn, e protocol.RegisterEvt) {
	unlock := h.state.lock(ac.serverID)
	defer unlock()
	if !h.isCurrentConnection(ac.serverID, ac) {
		return
	}
	serverID := ac.serverID
	ac.mu.Lock()
	ac.restrictedOutboundValidation = e.RestrictedOutboundValidation
	ac.mu.Unlock()
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
	h.db.Model(&model.Server{}).Where("id = ?", serverID).Updates(updates)
	fallbackIP := e.PublicIP
	if fallbackIP == "" && e.PublicIPv4 == nil {
		fallbackIP = ac.remoteIP
	}
	if h.updatePublicAddresses(serverID, fallbackIP, e.PublicIPv4, e.PublicIPv6) && h.AfterAddressChange != nil {
		h.AfterAddressChange(serverID)
	}
	if h.AfterRegister != nil {
		h.AfterRegister(serverID)
	}
}

func (h *Hub) onHeartbeat(ac *agentConn, e protocol.HeartbeatEvt) {
	if !h.isCurrentConnection(ac.serverID, ac) {
		return
	}
	serverID := ac.serverID
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
	capabilityChanged := false
	if e.SingboxVersion != "" {
		updates["singbox_version"] = e.SingboxVersion
		updates["singbox_installed"] = true
		var previous model.Server
		if h.db.Select("singbox_version").First(&previous, serverID).Error == nil &&
			previous.SingboxVersion != e.SingboxVersion &&
			statsAPISupported(previous.SingboxVersion) != statsAPISupported(e.SingboxVersion) {
			capabilityChanged = true
		}
	}
	h.db.Model(&model.Server{}).Where("id = ?", serverID).Updates(updates)
	if h.updatePublicAddresses(serverID, "", e.PublicIPv4, e.PublicIPv6) && h.AfterAddressChange != nil {
		h.AfterAddressChange(serverID)
	}
	if capabilityChanged && h.AfterStatsCapabilityChange != nil {
		h.AfterStatsCapabilityChange(serverID)
	}
	if err := h.traffic.record(serverID, e.Traffic); err != nil {
		log.Printf("traffic: record server %d: %v", serverID, err)
	}
	if e.Traffic == nil {
		h.live.publishTraffic(serverID, nil)
	}
	online := true
	load1 := e.Load1
	memUsed := e.MemUsed
	memTotal := e.MemTotal
	uptime := e.Uptime
	active := e.SingboxActive
	status := serverLiveStatus{
		ServerID:      serverID,
		Online:        &online,
		Load1:         &load1,
		MemUsed:       &memUsed,
		MemTotal:      &memTotal,
		Uptime:        &uptime,
		SingboxActive: &active,
	}
	if e.Traffic == nil || e.Traffic.Unavailable {
		available := false
		zero := uint64(0)
		status.TrafficAvailable = &available
		status.TrafficUploadRate = &zero
		status.TrafficDownloadRate = &zero
	} else {
		available := true
		up := e.Traffic.UploadRate
		down := e.Traffic.DownloadRate
		status.TrafficAvailable = &available
		status.TrafficUploadRate = &up
		status.TrafficDownloadRate = &down
	}
	h.live.publishServerStatus(status)
}
