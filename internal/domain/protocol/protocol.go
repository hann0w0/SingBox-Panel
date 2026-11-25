// Package protocol defines the fixed message set exchanged between the Panel
// and the Agents over the reverse WebSocket channel. The Agent never accepts
// arbitrary shell; every capability is a typed message here.
package protocol

import "encoding/json"

// LocalTrafficAddress is the loopback-only Clash API endpoint used by managed
// sing-box configs. It is never exposed outside the node.
const LocalTrafficAddress = "127.0.0.1:29091"

// LocalStatsAPIAddress is the loopback-only sing-box 1.14+ API service used for
// per-user connection accounting. Like LocalTrafficAddress it requires the
// node's stats secret and is never exposed outside the node.
const LocalStatsAPIAddress = "127.0.0.1:29092"

// MessageType enumerates the fixed command/event set.
type MessageType string

const (
	// Panel -> Agent commands.
	CmdInstallSingbox   MessageType = "install_singbox"
	CmdApplyConfig      MessageType = "apply_config"
	CmdServiceAction    MessageType = "service_action"
	CmdGetStatus        MessageType = "get_status"
	CmdGetConfig        MessageType = "get_config"
	CmdUpdateAgent      MessageType = "update_agent"
	CmdTestOutbound     MessageType = "test_outbound"
	CmdGetLogs          MessageType = "get_logs"
	CmdStreamLogs       MessageType = "stream_logs"
	CmdUninstallSingbox MessageType = "uninstall_singbox"
	CmdUninstallAgent   MessageType = "uninstall_agent"
	EvtTrafficAck       MessageType = "traffic_ack"

	// Agent -> Panel events.
	EvtRegister      MessageType = "register"
	EvtHeartbeat     MessageType = "heartbeat"
	EvtCommandResult MessageType = "command_result"
	EvtLog           MessageType = "log"
	EvtTraffic       MessageType = "traffic"
	EvtMetrics       MessageType = "metrics"
	EvtProgress      MessageType = "progress"
)

// Envelope is the wire format for every message.
type Envelope struct {
	Type    MessageType     `json:"type"`
	ID      string          `json:"id,omitempty"` // command correlation id (empty for spontaneous events)
	Payload json.RawMessage `json:"payload,omitempty"`
}

// NewEnvelope marshals payload into an Envelope. A nil payload yields no field.
func NewEnvelope(t MessageType, id string, payload any) (Envelope, error) {
	e := Envelope{Type: t, ID: id}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return Envelope{}, err
		}
		e.Payload = raw
	}
	return e, nil
}

// Decode unmarshals the envelope payload into v.
func (e Envelope) Decode(v any) error {
	if len(e.Payload) == 0 {
		return nil
	}
	return json.Unmarshal(e.Payload, v)
}

// ---- Panel -> Agent payloads ----

// InstallChannel selects the official release channel.
const (
	ChannelStable = "stable"
	ChannelBeta   = "beta"
)

// InstallMethod selects how the official package is fetched.
const (
	MethodScript = "script" // curl -fsSL https://sing-box.app/install.sh | sh (or official musl release on Alpine)
	MethodAPT    = "apt"    // official deb.sagernet.org repo
	MethodDNF    = "dnf"    // official rpm.sagernet.org repo
)

// InstallSingboxCmd installs the official sing-box.
type InstallSingboxCmd struct {
	Channel string `json:"channel"`           // stable | beta
	Version string `json:"version,omitempty"` // pin a specific version (script method only)
	Method  string `json:"method"`            // script | apt | dnf
}

// InstallSingboxData reports post-install binary details back to the panel.
type InstallSingboxData struct {
	SingboxVersion string `json:"singbox_version,omitempty"`
}

// ApplyConfigCmd delivers a full official config.json to write to /etc/sing-box.
type ApplyConfigCmd struct {
	Config                 json.RawMessage `json:"config"` // full sing-box config.json; Agent validates then restarts sing-box
	RestrictedOutboundTags []string        `json:"restricted_outbound_tags,omitempty"`
}

// Service actions map 1:1 to `systemctl <action> sing-box`.
const (
	SvcStart   = "start"
	SvcStop    = "stop"
	SvcRestart = "restart"
	SvcReload  = "reload"
	SvcEnable  = "enable"
	SvcDisable = "disable"
	SvcStatus  = "status"
)

// ServiceActionCmd performs a systemctl action on the official unit.
type ServiceActionCmd struct {
	Action string `json:"action"`
}

// TestOutboundCmd asks the agent to check reachability of a landing server
// FROM this node (a TCP connect, which — unlike ICMP — proves the actual port
// is open and is not blocked by the common "ping disabled" setup).
//
// QUIC-based landings (hysteria2 / tuic) listen on UDP only, so a TCP connect
// would always fail. For those the panel sets Network to "quic" and the agent
// sends a QUIC version-negotiation probe instead. Older agents ignore the
// field and keep doing a TCP connect.
type TestOutboundCmd struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Network string `json:"network,omitempty"` // "" (tcp) | "quic"
	// Obfuscated marks a QUIC server behind packet obfuscation (hysteria2
	// salamander/gecko). It answers nothing without the obfs key, so the probe
	// can only confirm that the address resolves.
	Obfuscated bool `json:"obfuscated,omitempty"`
}

// TestOutboundData is the reachability result.
type TestOutboundData struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
	// Inconclusive means the target could not be probed without speaking the
	// full protocol; Note explains what was (and was not) verified.
	Inconclusive bool   `json:"inconclusive,omitempty"`
	Note         string `json:"note,omitempty"`
}

// GetLogsCmd asks the agent for the tail of the sing-box service log.
type GetLogsCmd struct {
	Lines int `json:"lines,omitempty"` // default 200, capped agent-side
}

// LogsData carries the returned log text.
type LogsData struct {
	Text string `json:"text"`
}

// ---- Agent -> Panel payloads ----

// RegisterEvt is sent immediately after the WS handshake authenticates.
type RegisterEvt struct {
	AgentVersion                 string  `json:"agent_version"`
	Hostname                     string  `json:"hostname"`
	OS                           string  `json:"os"`
	Arch                         string  `json:"arch"`
	Kernel                       string  `json:"kernel"`
	PublicIP                     string  `json:"public_ip,omitempty"`
	PublicIPv4                   *string `json:"public_ipv4,omitempty"`
	PublicIPv6                   *string `json:"public_ipv6,omitempty"`
	SingboxInstalled             bool    `json:"singbox_installed"`
	SingboxVersion               string  `json:"singbox_version,omitempty"`
	PanelURL                     string  `json:"panel_url,omitempty"`
	RestrictedOutboundValidation bool    `json:"restricted_outbound_validation,omitempty"`
}

// HeartbeatEvt is sent periodically.
type HeartbeatEvt struct {
	PublicIPv4     *string          `json:"public_ipv4,omitempty"`
	PublicIPv6     *string          `json:"public_ipv6,omitempty"`
	TS             int64            `json:"ts"`
	Load1          float64          `json:"load1"`
	MemUsed        uint64           `json:"mem_used"`
	MemTotal       uint64           `json:"mem_total"`
	Uptime         int64            `json:"uptime"`
	SingboxActive  bool             `json:"singbox_active"`
	SingboxVersion string           `json:"singbox_version,omitempty"`
	Traffic        *TrafficSnapshot `json:"traffic,omitempty"`
}

// TrafficSnapshot contains sing-box's cumulative node counters and the
// per-inbound deltas observed since the previous Agent sample.
type TrafficSnapshot struct {
	// Version 2 accounts only timestamped, acknowledged batches. Heartbeat
	// counters are live telemetry, never an additional source of billable bytes.
	AccountingVersion int            `json:"accounting_version,omitempty"`
	Unavailable       bool           `json:"unavailable,omitempty"`
	Report            *TrafficReport `json:"report,omitempty"`
	UploadTotal       uint64         `json:"upload_total"`
	DownloadTotal     uint64         `json:"download_total"`
	UploadRate        uint64         `json:"upload_rate"`
	DownloadRate      uint64         `json:"download_rate"`
	TCPConnections    int            `json:"tcp_connections"`
	UDPConnections    int            `json:"udp_connections"`
	SampledAt         int64          `json:"sampled_at"`
	// PeakUploadRate/PeakDownloadRate are the highest one-second rates the
	// Agent observed since the previous acknowledged report. UploadRate and
	// DownloadRate remain the latest instantaneous values for live charts.
	// Older Agents omit them; the panel then falls back to the instant rate.
	PeakUploadRate   uint64                `json:"peak_upload_rate,omitempty"`
	PeakDownloadRate uint64                `json:"peak_download_rate,omitempty"`
	Ports            []PortTrafficSnapshot `json:"ports,omitempty"`
	// Users are per (inbound, authenticated user) byte deltas, reported only by
	// Agents that read the sing-box API service. Older Agents omit them.
	Users []UserTrafficSnapshot `json:"users,omitempty"`
}

// TrafficReport is immutable until acknowledged. Sequence starts at 1 for
// each Agent process session; resends carry the same session and sequence.
type TrafficReport struct {
	Session  string              `json:"session"`
	Sequence uint64              `json:"sequence"`
	Buckets  []TrafficTimeBucket `json:"buckets"`
}

type TrafficAck struct {
	Session    string `json:"session"`
	Sequence   uint64 `json:"sequence"`
	ServerTime int64  `json:"server_time,omitempty"`
}

// TrafficTimeBucket keeps collection time even when the control connection
// stays offline across UTC midnight or a monthly reset boundary.
type TrafficTimeBucket struct {
	Time           int64                 `json:"time"`
	Upload         uint64                `json:"upload"`
	Download       uint64                `json:"download"`
	UploadRate     uint64                `json:"upload_rate"`
	DownloadRate   uint64                `json:"download_rate"`
	TCPConnections int                   `json:"tcp_connections"`
	UDPConnections int                   `json:"udp_connections"`
	Ports          []PortTrafficSnapshot `json:"ports,omitempty"`
	Users          []UserTrafficSnapshot `json:"users,omitempty"`
}

// UserTrafficSnapshot is a delta for one authenticated proxy user on one
// inbound tag, not a cumulative counter.
type UserTrafficSnapshot struct {
	Inbound  string `json:"inbound"`
	User     string `json:"user"`
	Upload   uint64 `json:"upload"`
	Download uint64 `json:"download"`
}

// PortTrafficSnapshot is a delta for one sing-box inbound tag, not a
// cumulative counter. The panel aggregates it into time buckets.
type PortTrafficSnapshot struct {
	Inbound      string `json:"inbound"`
	Upload       uint64 `json:"upload"`
	Download     uint64 `json:"download"`
	UploadRate   uint64 `json:"upload_rate"`
	DownloadRate uint64 `json:"download_rate"`
}

// CommandResultEvt is the reply to a Panel command, correlated by ID.
type CommandResultEvt struct {
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Output string          `json:"output,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// InboundBrief summarizes one inbound found in an existing config.
type InboundBrief struct {
	Tag        string `json:"tag"`
	Type       string `json:"type"`
	ListenPort int    `json:"listen_port"`
}

// StatusData is the Data payload of a get_status command_result.
type StatusData struct {
	AgentVersion     string         `json:"agent_version,omitempty"`
	SingboxInstalled bool           `json:"singbox_installed"`
	SingboxVersion   string         `json:"singbox_version"`
	ServiceActive    bool           `json:"service_active"`
	ServiceEnabled   bool           `json:"service_enabled"`
	HasConfig        bool           `json:"has_config"`
	Inbounds         []InboundBrief `json:"inbounds,omitempty"`
	Hostname         string         `json:"hostname,omitempty"`
	OS               string         `json:"os,omitempty"`
	Arch             string         `json:"arch,omitempty"`
	Kernel           string         `json:"kernel,omitempty"`
	PublicIP         string         `json:"public_ip,omitempty"`
	PublicIPv4       *string        `json:"public_ipv4,omitempty"`
	PublicIPv6       *string        `json:"public_ipv6,omitempty"`
	Load1            float64        `json:"load1,omitempty"`
	MemUsed          uint64         `json:"mem_used,omitempty"`
	MemTotal         uint64         `json:"mem_total,omitempty"`
	Uptime           int64          `json:"uptime,omitempty"`
}

// ConfigData is the Data payload of a get_config command_result: the current
// /etc/sing-box/config.json read from the host (for viewing/importing an
// existing sing-box installation).
type ConfigData struct {
	Path     string         `json:"path"`
	Exists   bool           `json:"exists"`
	Raw      string         `json:"raw"`
	Inbounds []InboundBrief `json:"inbounds,omitempty"`
}

// LogEvt streams an agent-side log line to the panel.
type LogEvt struct {
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// ---- New payloads: traffic / progress / log streaming ----

// TrafficEvt is a high-frequency traffic snapshot sent independently of the
// heartbeat, allowing 2-3s reporting without bloating the heartbeat payload.
type TrafficEvt struct {
	Traffic *TrafficSnapshot `json:"traffic,omitempty"`
}

// MetricsEvt is a lightweight 1s live telemetry frame sent to keep the admin UI
// responsive without triggering database writes or subprocess probes.
type MetricsEvt struct {
	Load1            float64 `json:"load1"`
	MemUsed          uint64  `json:"mem_used"`
	MemTotal         uint64  `json:"mem_total"`
	TrafficAvailable bool    `json:"traffic_available"`
	UploadRate       uint64  `json:"upload_rate"`
	DownloadRate     uint64  `json:"download_rate"`
}

// ProgressEvt is sent during long operations (install, self-update) so the
// panel can show real-time output before the final CommandResult arrives.
type ProgressEvt struct {
	ID     string `json:"id"`               // command correlation id
	Line   string `json:"line"`             // one line of output
	Stream string `json:"stream,omitempty"` // "stdout" | "stderr" | ""
}

// StreamLogsCmd starts or stops continuous log streaming from the sing-box service.
type StreamLogsCmd struct {
	Enable    bool   `json:"enable"`               // true = start streaming, false = stop
	Lines     int    `json:"lines,omitempty"`      // initial tail lines when enabling
	SessionID string `json:"session_id,omitempty"` // panel-side browser session reference
}
