package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

// Config configures an Agent.
type Config struct {
	PanelURL     string
	Token        string
	Insecure     bool
	AgentVersion string
}

// opsGate serializes operations that mutate the sing-box installation or the
// Agent itself. Unlike a mutex, waiting for this gate observes the command
// context, so one stuck package manager cannot queue later operations forever.
var opsGate = make(chan struct{}, 1)

// Agent wires the command handlers and stats collector to the WS client.
type Agent struct {
	cfg       Config
	client    *Client
	traffic   *trafficSampler
	logStream *logStreamer
	// Only one delta-bearing report may be queued until its write completes.
	trafficReportInFlight atomic.Bool
	addressMu             sync.Mutex
	addressProbeAt        time.Time
}

// New builds an Agent and wires the client callbacks.
func New(cfg Config) *Agent {
	a := &Agent{
		cfg:       cfg,
		traffic:   newTrafficSampler(),
		logStream: newLogStreamer(),
	}
	a.client = NewClient(cfg.PanelURL, cfg.Token, cfg.Insecure)
	a.client.Register = a.register
	a.client.Heartbeat = a.heartbeat
	a.client.OnCommand = a.onCommand
	a.client.OnTrafficAck = a.traffic.acknowledgeReport
	a.client.OnConnected = func() {
		if err := markAgentReady(a.cfg.AgentVersion); err != nil {
			log.Printf("agent: write readiness marker: %v", err)
		}
	}
	a.client.OnDisconnected = a.logStream.stop
	return a
}

// Run blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) {
	go a.traffic.run(ctx)
	// sing-box 1.14+ API service: per-user attribution and exact final totals
	// for closing connections. Without it the Clash poll alone keeps per-port
	// accounting working.
	go newStatsStream(a.traffic).run(ctx)
	// Push high-frequency traffic snapshots every 3s, independent of the 10s
	// heartbeat. This lets the panel capture short-lived rate spikes without
	// bloating the heartbeat payload.
	go a.trafficReportLoop(ctx)
	// Push lightweight 1s live metrics (rates + /proc load/memory) for smooth UI
	// updates. Handled purely in memory on the panel without database writes.
	go a.liveMetricsLoop(ctx)
	// Ensure log stream stops when the agent exits.
	go func() {
		<-ctx.Done()
		a.logStream.stop()
	}()
	a.client.Run(ctx)
}

// trafficReportLoop sends a TrafficEvt every 3 seconds. The heartbeat still
// carries a snapshot too, so the panel always gets at least one per 10s even
// if this loop is starved.
func (a *Agent) trafficReportLoop(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.reportTraffic()
		}
	}
}

func (a *Agent) reportTraffic() {
	if !a.trafficReportInFlight.CompareAndSwap(false, true) {
		return
	}
	snap := a.traffic.accountingSnapshot()
	if snap == nil {
		a.trafficReportInFlight.Store(false)
		return
	}
	if !a.client.sendEvent(protocol.EvtTraffic, protocol.TrafficEvt{Traffic: snap}, func(_ bool) {
		// A socket receipt only releases the queue slot. The batch is retained
		// until a matching traffic_ack confirms database commit on the panel.
		a.trafficReportInFlight.Store(false)
	}) {
		a.trafficReportInFlight.Store(false)
	}
}

func (a *Agent) liveMetricsLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.reportLiveMetrics()
		}
	}
}

func (a *Agent) reportLiveMetrics() {
	memUsed, memTotal := memInfo()
	available, upRate, downRate := a.traffic.liveRates()
	a.client.sendEvent(protocol.EvtMetrics, protocol.MetricsEvt{
		Load1:            loadAvg1(),
		MemUsed:          memUsed,
		MemTotal:         memTotal,
		TrafficAvailable: available,
		UploadRate:       upRate,
		DownloadRate:     downRate,
	}, nil)
}

func (a *Agent) register() protocol.RegisterEvt {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	installed, ver := DetectVersion(ctx)
	si := CollectSysInfo(ctx)
	addresses := a.publicAddresses(ctx, true)
	return protocol.RegisterEvt{
		AgentVersion:                 a.cfg.AgentVersion,
		Hostname:                     si.Hostname,
		OS:                           si.OS,
		Arch:                         si.Arch,
		Kernel:                       si.Kernel,
		PublicIPv4:                   addresses.IPv4,
		PublicIPv6:                   addresses.IPv6,
		PublicIP:                     addressValue(addresses.IPv4),
		SingboxInstalled:             installed,
		SingboxVersion:               ver,
		PanelURL:                     a.cfg.PanelURL,
		RestrictedOutboundValidation: true,
	}
}

func commandMutates(t protocol.MessageType) bool {
	switch t {
	case protocol.CmdInstallSingbox, protocol.CmdApplyConfig,
		protocol.CmdServiceAction, protocol.CmdUpdateAgent,
		protocol.CmdUninstallSingbox, protocol.CmdUninstallAgent:
		return true
	default:
		return false
	}
}

// commandTimeout is deliberately shorter than the matching Panel HTTP timeout,
// leaving enough time to send a failure result before the Panel gives up.
func commandTimeout(t protocol.MessageType) time.Duration {
	switch t {
	case protocol.CmdInstallSingbox:
		return 5*time.Minute + 30*time.Second
	case protocol.CmdApplyConfig:
		return 50 * time.Second
	case protocol.CmdServiceAction:
		return 25 * time.Second
	case protocol.CmdUpdateAgent:
		return 2*time.Minute + 30*time.Second
	case protocol.CmdUninstallSingbox:
		return 2 * time.Minute
	case protocol.CmdUninstallAgent:
		return 30 * time.Second
	case protocol.CmdGetLogs:
		return 25 * time.Second
	case protocol.CmdTestOutbound:
		return 15 * time.Second
	case protocol.CmdGetStatus, protocol.CmdGetConfig:
		return 12 * time.Second
	case protocol.CmdStreamLogs:
		return 10 * time.Second
	default:
		return 15 * time.Second
	}
}

func (a *Agent) heartbeat() protocol.HeartbeatEvt {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	si := CollectSysInfo(ctx)
	_, ver := DetectVersion(ctx)
	addresses := a.publicAddresses(ctx, false)
	event := protocol.HeartbeatEvt{
		PublicIPv4:     addresses.IPv4,
		PublicIPv6:     addresses.IPv6,
		TS:             time.Now().Unix(),
		Load1:          si.Load1,
		MemUsed:        si.MemUsed,
		MemTotal:       si.MemTotal,
		Uptime:         si.Uptime,
		SingboxActive:  ServiceActive(ctx),
		SingboxVersion: ver,
		Traffic:        a.traffic.summarySnapshot(),
	}
	return event
}

func (a *Agent) onCommand(ctx context.Context, env protocol.Envelope) protocol.CommandResultEvt {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout(env.Type))
	defer cancel()
	if commandMutates(env.Type) {
		select {
		case opsGate <- struct{}{}:
			defer func() { <-opsGate }()
		case <-ctx.Done():
			return cmdResult(env.ID, "", fmt.Errorf("wait for previous operation: %w", ctx.Err()))
		}
	}
	switch env.Type {
	case protocol.CmdInstallSingbox:
		var c protocol.InstallSingboxCmd
		if err := env.Decode(&c); err != nil {
			return decodeResult(env.ID, err)
		}
		a.client.SendEventForContext(ctx, protocol.EvtProgress, env.ID, protocol.ProgressEvt{ID: env.ID, Line: "开始安装 sing-box...", Stream: "stdout"})
		out, err := InstallSingbox(ctx, c.Channel, c.Version, c.Method)
		if err != nil {
			a.client.SendEventForContext(ctx, protocol.EvtProgress, env.ID, protocol.ProgressEvt{ID: env.ID, Line: err.Error(), Stream: "stderr"})
		}
		return cmdResult(env.ID, out, err)

	case protocol.CmdApplyConfig:
		var c protocol.ApplyConfigCmd
		if err := env.Decode(&c); err != nil {
			return decodeResult(env.ID, err)
		}
		out, err := applyRequestedConfig(ctx, c, resolveRestrictedOutbound, ApplyConfig, requestedConfigSourceFile)
		return cmdResult(env.ID, out, err)

	case protocol.CmdServiceAction:
		var c protocol.ServiceActionCmd
		if err := env.Decode(&c); err != nil {
			return decodeResult(env.ID, err)
		}
		out, err := ServiceAction(ctx, c.Action)
		return cmdResult(env.ID, out, err)

	case protocol.CmdTestOutbound:
		var c protocol.TestOutboundCmd
		if err := env.Decode(&c); err != nil {
			return decodeResult(env.ID, err)
		}
		data, _ := json.Marshal(TestOutbound(ctx, c))
		return protocol.CommandResultEvt{ID: env.ID, OK: true, Data: data}

	case protocol.CmdGetLogs:
		var c protocol.GetLogsCmd
		if err := env.Decode(&c); err != nil {
			return decodeResult(env.ID, err)
		}
		text, err := ReadLogs(ctx, c.Lines)
		if err != nil {
			return cmdResult(env.ID, "", err)
		}
		data, _ := json.Marshal(protocol.LogsData{Text: text})
		return protocol.CommandResultEvt{ID: env.ID, OK: true, Data: data}

	case protocol.CmdStreamLogs:
		var c protocol.StreamLogsCmd
		if err := env.Decode(&c); err != nil {
			return decodeResult(env.ID, err)
		}
		if c.Enable {
			session, _ := ctx.Value(clientSessionContextKey{}).(uint64)
			err := a.logStream.start(c.Lines, func(line string) {
				a.client.SendEventForSession(session, protocol.EvtLog, env.ID, protocol.LogEvt{Level: "info", Msg: line})
			})
			if err != nil {
				return cmdResult(env.ID, "", err)
			}
			return protocol.CommandResultEvt{ID: env.ID, OK: true, Output: "log streaming started"}
		}
		a.logStream.stop()
		return protocol.CommandResultEvt{ID: env.ID, OK: true, Output: "log streaming stopped"}

	case protocol.CmdUpdateAgent:
		a.client.SendEventForContext(ctx, protocol.EvtProgress, env.ID, protocol.ProgressEvt{ID: env.ID, Line: "开始更新 Agent...", Stream: "stdout"})
		out, err := SelfUpdate(ctx, a.cfg.PanelURL, a.client.Token(), a.cfg.Insecure)
		if err != nil {
			a.client.SendEventForContext(ctx, protocol.EvtProgress, env.ID, protocol.ProgressEvt{ID: env.ID, Line: err.Error(), Stream: "stderr"})
		}
		return cmdResult(env.ID, out, err)

	case protocol.CmdUninstallSingbox:
		out, err := UninstallSingbox(ctx)
		return cmdResult(env.ID, out, err)

	case protocol.CmdUninstallAgent:
		out, err := ScheduleSelfUninstall(ctx)
		return cmdResult(env.ID, out, err)

	case protocol.CmdGetStatus:
		installed, ver := DetectVersion(ctx)
		si := CollectSysInfo(ctx)
		_, briefs, hasCfg := ReadConfig()
		addresses := a.publicAddresses(ctx, true)
		data, _ := json.Marshal(protocol.StatusData{
			AgentVersion:     a.cfg.AgentVersion,
			SingboxInstalled: installed,
			SingboxVersion:   ver,
			ServiceActive:    ServiceActive(ctx),
			ServiceEnabled:   ServiceEnabled(ctx),
			HasConfig:        hasCfg,
			Inbounds:         briefs,
			Hostname:         si.Hostname,
			OS:               si.OS,
			Arch:             si.Arch,
			Kernel:           si.Kernel,
			PublicIPv4:       addresses.IPv4,
			PublicIPv6:       addresses.IPv6,
			PublicIP:         addressValue(addresses.IPv4),
			Load1:            si.Load1,
			MemUsed:          si.MemUsed,
			MemTotal:         si.MemTotal,
			Uptime:           si.Uptime,
		})
		return protocol.CommandResultEvt{ID: env.ID, OK: true, Data: data}

	case protocol.CmdGetConfig:
		raw, briefs, exists := ReadConfig()
		if exists {
			raw = readRequestedConfig(raw)
		}
		data, _ := json.Marshal(protocol.ConfigData{
			Path:     ConfigFile,
			Exists:   exists,
			Raw:      string(raw),
			Inbounds: briefs,
		})
		return protocol.CommandResultEvt{ID: env.ID, OK: true, Data: data}

	default:
		return protocol.CommandResultEvt{ID: env.ID, OK: false, Error: "unknown command " + string(env.Type)}
	}
}

func (a *Agent) publicAddresses(ctx context.Context, force bool) publicAddresses {
	a.addressMu.Lock()
	defer a.addressMu.Unlock()
	if !force && time.Since(a.addressProbeAt) < 30*time.Second {
		return publicAddresses{}
	}
	addresses := detectPublicAddresses(ctx)
	a.addressProbeAt = time.Now()
	return addresses
}

func addressValue(address *string) string {
	if address == nil {
		return ""
	}
	return *address
}

func decodeResult(id string, err error) protocol.CommandResultEvt {
	return cmdResult(id, "", fmt.Errorf("decode command payload: %w", err))
}

func cmdResult(id, out string, err error) protocol.CommandResultEvt {
	if err != nil {
		return protocol.CommandResultEvt{ID: id, OK: false, Error: err.Error(), Output: out}
	}
	return protocol.CommandResultEvt{ID: id, OK: true, Output: out}
}
