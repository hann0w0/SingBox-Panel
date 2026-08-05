// Package model holds the persisted entities (GORM) shared across the panel.
package model

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"time"
)

// JSONText is a JSON-valued DB column stored as text, usable as json.RawMessage.
type JSONText []byte

// Value implements driver.Valuer.
func (j JSONText) Value() (driver.Value, error) {
	if len(j) == 0 {
		return "null", nil
	}
	return string(j), nil
}

// Scan implements sql.Scanner.
func (j *JSONText) Scan(src any) error {
	if src == nil {
		*j = nil
		return nil
	}
	switch v := src.(type) {
	case []byte:
		*j = append((*j)[:0], v...)
	case string:
		*j = append((*j)[:0], v...)
	default:
		return errors.New("model.JSONText: unsupported Scan source")
	}
	return nil
}

// MarshalJSON returns the raw JSON.
func (j JSONText) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	return j, nil
}

// UnmarshalJSON stores the raw JSON.
func (j *JSONText) UnmarshalJSON(data []byte) error {
	if j == nil {
		return errors.New("model.JSONText: UnmarshalJSON on nil pointer")
	}
	*j = append((*j)[:0], data...)
	return nil
}

// Role is a panel account role.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// User is a panel account. On a multi-user-capable inbound its ProxyToken also
// seeds a stable, per-inbound proxy identity; single-credential inbounds still
// use the credential stored in their own settings.
type User struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Email    string `gorm:"uniqueIndex;size:191" json:"email"`
	Password string `json:"-"` // bcrypt hash
	Role     Role   `gorm:"size:16;index" json:"role"`

	// ServerIDs are the nodes this user is provisioned on and can subscribe to.
	ServerIDs []uint `gorm:"serializer:json" json:"server_ids"`

	// InboundIDs narrows access to specific protocols within those nodes — e.g.
	// a home-broadband inbound kept for personal use only. Empty means every
	// inbound on the selected servers (the previous, server-level behaviour).
	InboundIDs []uint `gorm:"serializer:json" json:"inbound_ids"`

	ExpireAt *time.Time `json:"expire_at"` // nil = never

	Enabled  bool   `gorm:"index" json:"enabled"`
	SubToken string `gorm:"uniqueIndex;size:64" json:"sub_token"`
	// ProxyToken is an independent, stable seed for deterministic per-inbound
	// credentials. Resetting a subscription URL or changing a login password
	// therefore never breaks already-issued proxy credentials.
	ProxyToken string `gorm:"size:64" json:"-"`
	// TokenVersion revokes every previously-issued JWT when it changes. Password
	// changes, account disabling and other security-sensitive updates increment it.
	TokenVersion uint `json:"-"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const (
	ConfigModeManaged = "managed"
	ConfigModeRaw     = "raw"
)

// Server is one VPS, i.e. one Agent.
type Server struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64" json:"name"`

	// Connection identity: the agent authenticates with this bearer token.
	AgentToken string `gorm:"uniqueIndex;size:64" json:"-"`

	// Public host used in share/subscription links (domain or IP the clients dial).
	Address string `gorm:"size:191" json:"address"`
	Remark  string `gorm:"size:255" json:"remark"`
	Region  string `gorm:"size:64" json:"region"`

	// Reported by the agent.
	Online               bool       `gorm:"index" json:"online"`
	LastSeen             *time.Time `json:"last_seen"`
	Hostname             string     `gorm:"size:191" json:"hostname"`
	OS                   string     `gorm:"size:64" json:"os"`
	Arch                 string     `gorm:"size:32" json:"arch"`
	Kernel               string     `gorm:"size:64" json:"kernel"`
	PublicIP             string     `gorm:"size:64" json:"public_ip"`
	AgentVersion         string     `gorm:"size:64" json:"agent_version"`
	SingboxInstalled     bool       `json:"singbox_installed"`
	SingboxVersion       string     `gorm:"size:32" json:"singbox_version"`
	SingboxActive        bool       `json:"singbox_active"`
	SingboxHasUpdate     bool       `gorm:"-" json:"singbox_has_update,omitempty"`
	SingboxLatestVersion string     `gorm:"-" json:"singbox_latest_version,omitempty"`
	Uptime               int64      `json:"uptime"`
	Load1                float64    `json:"load1"`
	MemUsed              uint64     `json:"mem_used"`
	MemTotal             uint64     `json:"mem_total"`

	// Traffic counters are accumulated from the Agent's loopback Clash API.
	TrafficAvailable      bool       `gorm:"index" json:"traffic_available"`
	TrafficUpload         uint64     `json:"traffic_upload"`
	TrafficDownload       uint64     `json:"traffic_download"`
	TrafficUploadRate     uint64     `json:"traffic_upload_rate"`
	TrafficDownloadRate   uint64     `json:"traffic_download_rate"`
	TrafficTCPConnections int        `json:"traffic_tcp_connections"`
	TrafficUDPConnections int        `json:"traffic_udp_connections"`
	TrafficUpdatedAt      *time.Time `json:"traffic_updated_at"`
	TrafficRemoteUpload   uint64     `json:"-"`
	TrafficRemoteDownload uint64     `json:"-"`

	// FinalOutbound is the default route target (default "direct").
	FinalOutbound string `gorm:"size:64" json:"final_outbound"`

	// ConfigMode selects the source of truth for the node configuration. Managed
	// mode renders the structured inbound/outbound/routing tables; raw mode keeps
	// and re-applies the administrator's complete JSON without dropping fields the
	// panel does not understand.
	ConfigMode string   `gorm:"size:16;default:managed" json:"config_mode"`
	RawConfig  JSONText `gorm:"type:text" json:"-"`
	// ConfigInitialized distinguishes a brand-new node (whose existing
	// sing-box config must not be touched) from a deliberately empty managed
	// config after the administrator deletes the last structured record.
	ConfigInitialized bool `gorm:"default:false" json:"-"`

	// AgentURL is the endpoint reported by the running Agent. It makes migrations
	// from an IP address to the panel domain visible and verifiable in the UI.
	AgentURL string `gorm:"size:512" json:"agent_url"`

	Inbounds []Inbound `gorm:"constraint:OnDelete:CASCADE" json:"inbounds,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EffectiveConfigMode treats rows created before ConfigMode existed as managed.
func (s *Server) EffectiveConfigMode() string {
	if s.ConfigMode == ConfigModeRaw && len(s.RawConfig) > 0 {
		return ConfigModeRaw
	}
	return ConfigModeManaged
}

// InboundType is a supported server-side protocol.
type InboundType string

const (
	InboundVLESS       InboundType = "vless"
	InboundVMess       InboundType = "vmess"
	InboundTrojan      InboundType = "trojan"
	InboundShadowsocks InboundType = "shadowsocks"
	InboundHysteria2   InboundType = "hysteria2"
	InboundTUIC        InboundType = "tuic"
	InboundNaive       InboundType = "naive"
	InboundHysteria    InboundType = "hysteria" // v1
	InboundSocks       InboundType = "socks"
	InboundShadowTLS   InboundType = "shadowtls"
	InboundAnyTLS      InboundType = "anytls"
	InboundSnell       InboundType = "snell"
)

// Inbound is a protocol listener on a Server. Protocol/transport/tls specifics
// live in Settings as JSON, validated against internal/singbox.InboundSettings.
type Inbound struct {
	ID         uint        `gorm:"primaryKey" json:"id"`
	ServerID   uint        `gorm:"index" json:"server_id"`
	Tag        string      `gorm:"size:64" json:"tag"`
	Type       InboundType `gorm:"size:32" json:"type"`
	ListenPort int         `json:"listen_port"`
	Enabled    bool        `gorm:"index" json:"enabled"`
	Settings   JSONText    `gorm:"type:text" json:"settings"`
	Remark     string      `gorm:"size:255" json:"remark"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Outbound is a routing target on a Server: "direct" or a proxy to a landing
// server. Settings holds proxy-specific fields as JSON.
type Outbound struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ServerID  uint      `gorm:"index" json:"server_id"`
	Tag       string    `gorm:"size:64" json:"tag"`
	Type      string    `gorm:"size:32" json:"type"` // direct | vless | vmess | trojan | shadowsocks | hysteria2 | tuic
	Settings  JSONText  `gorm:"type:text" json:"settings"`
	Remark    string    `gorm:"size:255" json:"remark"`
	Sort      int       `json:"sort"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RouteRule is one routing rule on a Server: match conditions -> outbound tag
// (or "block"). Match holds the match fields as JSON.
type RouteRule struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ServerID  uint      `gorm:"index" json:"server_id"`
	Sort      int       `json:"sort"`
	Match     JSONText  `gorm:"type:text" json:"match"`
	Outbound  string    `gorm:"size:64" json:"outbound"`
	Remark    string    `gorm:"size:255" json:"remark"`
	Enabled   bool      `gorm:"index" json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RuleSet is a remote or local rule-set definition on a Server (for geoip/geosite-style
// matching referenced by route rules).
type RuleSet struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	ServerID       uint      `gorm:"index;uniqueIndex:idx_node_tag" json:"server_id"`
	Tag            string    `gorm:"size:64;not null;uniqueIndex:idx_node_tag" json:"tag"`
	Type           string    `gorm:"size:16;not null;default:remote" json:"type"`   // remote | local
	Format         string    `gorm:"size:16;not null;default:binary" json:"format"` // binary | source
	URL            string    `gorm:"size:512" json:"url"`
	Path           string    `gorm:"size:512" json:"path"`
	DownloadDetour string    `gorm:"size:64" json:"download_detour"`
	UpdateInterval string    `gorm:"size:32" json:"update_interval"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Setting is a KV configuration row.
type Setting struct {
	Key   string `gorm:"primaryKey;size:64" json:"key"`
	Value string `json:"value"`
}

// TrafficRecord is one five-minute accounting bucket. InboundID=0 stores the
// node total; non-zero rows are attributed to a sing-box inbound port.
type TrafficRecord struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	ServerID       uint      `gorm:"not null;uniqueIndex:idx_traffic_bucket,priority:1;index" json:"server_id"`
	InboundID      uint      `gorm:"not null;default:0;uniqueIndex:idx_traffic_bucket,priority:2;index" json:"inbound_id"`
	Bucket         time.Time `gorm:"not null;uniqueIndex:idx_traffic_bucket,priority:3;index" json:"bucket"`
	Upload         uint64    `gorm:"not null;default:0" json:"upload"`
	Download       uint64    `gorm:"not null;default:0" json:"download"`
	UploadRate     uint64    `gorm:"not null;default:0" json:"upload_rate"`
	DownloadRate   uint64    `gorm:"not null;default:0" json:"download_rate"`
	TCPConnections int       `gorm:"not null;default:0" json:"tcp_connections"`
	UDPConnections int       `gorm:"not null;default:0" json:"udp_connections"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// SchemaMigration records one successfully-applied database schema change.
// It deliberately lives outside AllModels: the migration runner bootstraps
// this table before it applies any application schema.
type SchemaMigration struct {
	Version   uint      `gorm:"primaryKey" json:"version"`
	Name      string    `gorm:"size:191;not null" json:"name"`
	Dirty     bool      `gorm:"not null;default:false" json:"dirty"`
	AppliedAt time.Time `gorm:"not null" json:"applied_at"`
}

// CustomNode is a hand-added external node that is merged into a user's
// subscription. It is defined either by a standard share link (vless://,
// vmess://, ss://, trojan://, hysteria2://, tuic://, anytls://, socks5://) or
// by structured fields for protocols without a widely-supported share-link
// scheme (snell, mixed). Every subscription format (links/sing-box/clash/
// surge) is derived from one of those two representations. The node is never
// part of the managed sing-box configuration — the panel cannot provision
// credentials on a server it does not control, so the link/params carry the
// complete client credentials. AllUsers keeps the default-for-future-users
// behaviour explicit; ExcludedUserIDs records per-user exceptions without
// collapsing that default into a snapshot of the current account list.
type CustomNode struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	AllUsers        bool      `gorm:"not null;default:false" json:"all_users"`
	UserIDs         []uint    `gorm:"serializer:json" json:"user_ids"`
	ExcludedUserIDs []uint    `gorm:"serializer:json" json:"excluded_user_ids"`
	Name            string    `gorm:"size:128" json:"name"`
	Link            string    `gorm:"size:1024" json:"link"`   // share link (optional)
	Protocol        string    `gorm:"size:32" json:"protocol"` // structured node protocol, e.g. snell
	Address         string    `gorm:"size:255" json:"address"` // structured node host
	Port            int       `json:"port"`
	Params          JSONText  `gorm:"type:text" json:"params"` // protocol-specific JSON (psk, version, obfs...)
	Enabled         bool      `gorm:"index" json:"enabled"`
	SortOrder       int       `json:"sort_order"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// AllModels lists every entity for AutoMigrate.
func AllModels() []any {
	return []any{
		&User{}, &Server{}, &Inbound{}, &Outbound{}, &RouteRule{}, &RuleSet{}, &Setting{}, &TrafficRecord{}, &CustomNode{},
	}
}

// HasServer reports whether the user is provisioned on the given server.
func (u *User) HasServer(id uint) bool {
	for _, sid := range u.ServerIDs {
		if sid == id {
			return true
		}
	}
	return false
}

// HasInbound reports whether the user may use one specific inbound. The user
// must hold the server, and — when a per-inbound selection exists — that
// inbound must be in it. An empty selection means all of the server's inbounds.
func (u *User) HasInbound(serverID, inboundID uint) bool {
	if !u.HasServer(serverID) {
		return false
	}
	if len(u.InboundIDs) == 0 {
		return true
	}
	for _, id := range u.InboundIDs {
		if id == inboundID {
			return true
		}
	}
	return false
}

// HasUser reports whether a custom node is assigned to one account.
func (n *CustomNode) HasUser(userID uint) bool {
	ids := n.UserIDs
	granted := false
	if n.AllUsers {
		ids = n.ExcludedUserIDs
		granted = true
	}
	for _, id := range ids {
		if id == userID {
			return !n.AllUsers
		}
	}
	return granted
}

// Expired reports whether the user's validity period has passed.
//
// Scope: expiry gates panel access and subscriptions. Managed multi-user
// inbounds also remove expired users during reconciliation. A protocol kept in
// single-credential mode cannot revoke one user without rotating its shared
// credential. Exact per-user traffic quotas remain unavailable until the
// installed sing-box build exposes authenticated-user counters.
func (u *User) Expired(now time.Time) bool {
	return u.ExpireAt != nil && now.After(*u.ExpireAt)
}

// String helpers for logging.
func (s *Server) String() string {
	return fmt.Sprintf("Server#%d(%s)", s.ID, s.Name)
}
