package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/hann0w0/singbox-panel/internal/protocol"
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
	a.client.OnConnected = func() {
		if err := markAgentReady(a.cfg.AgentVersion); err != nil {
			log.Printf("agent: write readiness marker: %v", err)
		}
	}
	return a
}

// Run blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) {
	go a.traffic.run(ctx)
	// Push high-frequency traffic snapshots every 3s, independent of the 10s
	// heartbeat. This lets the panel capture short-lived rate spikes without
	// bloating the heartbeat payload.
	go a.trafficReportLoop(ctx)
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
			snap := a.traffic.snapshot()
			if snap != nil {
				a.client.SendEvent(protocol.EvtTraffic, protocol.TrafficEvt{Traffic: snap})
			}
		}
	}
}

func (a *Agent) register() protocol.RegisterEvt {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	installed, ver := DetectVersion(ctx)
	si := CollectSysInfo(ctx)
	return protocol.RegisterEvt{
		AgentVersion:     a.cfg.AgentVersion,
		Hostname:         si.Hostname,
		OS:               si.OS,
		Arch:             si.Arch,
		Kernel:           si.Kernel,
		PublicIP:         DetectPublicIPv4(ctx),
		SingboxInstalled: installed,
		SingboxVersion:   ver,
		PanelURL:         a.cfg.PanelURL,
	}
}

func commandMutates(t protocol.MessageType) bool {
	switch t {
	case protocol.CmdInstallSingbox, protocol.CmdApplyConfig,
		protocol.CmdServiceAction, protocol.CmdUpdateAgent,
		protocol.CmdUninstallAgent:
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
	case protocol.CmdUninstallAgent:
		return 30 * time.Second
	case protocol.CmdGetLogs:
		return 25 * time.Second
	case protocol.CmdTestOutbound, protocol.CmdTestEgress:
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
	event := protocol.HeartbeatEvt{
		TS:             time.Now().Unix(),
		Load1:          si.Load1,
		MemUsed:        si.MemUsed,
		MemTotal:       si.MemTotal,
		Uptime:         si.Uptime,
		SingboxActive:  ServiceActive(ctx),
		SingboxVersion: ver,
		Traffic:        a.traffic.snapshot(),
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
		a.client.SendEventFor(protocol.EvtProgress, env.ID, protocol.ProgressEvt{ID: env.ID, Line: "开始安装 sing-box...", Stream: "stdout"})
		out, err := InstallSingbox(ctx, c.Channel, c.Version, c.Method)
		if err != nil {
			a.client.SendEventFor(protocol.EvtProgress, env.ID, protocol.ProgressEvt{ID: env.ID, Line: err.Error(), Stream: "stderr"})
		}
		return cmdResult(env.ID, out, err)

	case protocol.CmdApplyConfig:
		var c protocol.ApplyConfigCmd
		if err := env.Decode(&c); err != nil {
			return decodeResult(env.ID, err)
		}
		out, err := ApplyConfig(ctx, c.Config, c.Reload)
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
		data, _ := json.Marshal(TestOutbound(ctx, c.Host, c.Port))
		return protocol.CommandResultEvt{ID: env.ID, OK: true, Data: data}

	case protocol.CmdTestEgress:
		var c protocol.TestEgressCmd
		if err := env.Decode(&c); err != nil {
			return decodeResult(env.ID, err)
		}
		data, _ := json.Marshal(TestEgress(ctx, c.URL))
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
			err := a.logStream.start(c.Lines, func(line string) {
				a.client.SendEventFor(protocol.EvtLog, env.ID, protocol.LogEvt{Level: "info", Msg: line})
			})
			if err != nil {
				return cmdResult(env.ID, "", err)
			}
			return protocol.CommandResultEvt{ID: env.ID, OK: true, Output: "log streaming started"}
		}
		a.logStream.stop()
		return protocol.CommandResultEvt{ID: env.ID, OK: true, Output: "log streaming stopped"}

	case protocol.CmdUpdateAgent:
		a.client.SendEventFor(protocol.EvtProgress, env.ID, protocol.ProgressEvt{ID: env.ID, Line: "开始更新 Agent...", Stream: "stdout"})
		out, err := SelfUpdate(ctx, a.cfg.PanelURL, a.cfg.Insecure)
		if err != nil {
			a.client.SendEventFor(protocol.EvtProgress, env.ID, protocol.ProgressEvt{ID: env.ID, Line: err.Error(), Stream: "stderr"})
		}
		return cmdResult(env.ID, out, err)

	case protocol.CmdUninstallAgent:
		out, err := ScheduleSelfUninstall(ctx)
		return cmdResult(env.ID, out, err)

	case protocol.CmdGetStatus:
		installed, ver := DetectVersion(ctx)
		si := CollectSysInfo(ctx)
		_, briefs, hasCfg := ReadConfig()
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
			PublicIP:         DetectPublicIPv4(ctx),
			Load1:            si.Load1,
			MemUsed:          si.MemUsed,
			MemTotal:         si.MemTotal,
			Uptime:           si.Uptime,
		})
		return protocol.CommandResultEvt{ID: env.ID, OK: true, Data: data}

	case protocol.CmdGetConfig:
		raw, briefs, exists := ReadConfig()
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

func decodeResult(id string, err error) protocol.CommandResultEvt {
	return cmdResult(id, "", fmt.Errorf("decode command payload: %w", err))
}

func cmdResult(id, out string, err error) protocol.CommandResultEvt {
	if err != nil {
		return protocol.CommandResultEvt{ID: id, OK: false, Error: err.Error(), Output: out}
	}
	return protocol.CommandResultEvt{ID: id, OK: true, Output: out}
}
