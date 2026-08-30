package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

// Orchestrator builds server configs from the database and pushes them to agents.
type Orchestrator struct {
	db    *gorm.DB
	hub   *Hub
	locks *keyedMutex[uint]

	asyncMu      sync.Mutex
	asyncRunning map[uint]bool
	asyncPending map[uint]bool
}

type ConfigApplyState string

const (
	ConfigApplied ConfigApplyState = "applied"
	ConfigPending ConfigApplyState = "pending"
	ConfigFailed  ConfigApplyState = "failed"
)

// ConfigApplyResult separates persistence from delivery. Structured edits are
// always saved first; callers can then tell the UI whether the desired config
// is active, waiting for an offline Agent, or rejected by an online node.
type ConfigApplyResult struct {
	ApplyState ConfigApplyState `json:"apply_state"`
	ApplyError string           `json:"apply_error,omitempty"`
}

// NewOrchestrator builds an Orchestrator.
func NewOrchestrator(db *gorm.DB, hub *Hub) *Orchestrator {
	return &Orchestrator{
		db: db, hub: hub, locks: newKeyedMutex[uint](),
		asyncRunning: make(map[uint]bool), asyncPending: make(map[uint]bool),
	}
}

func (o *Orchestrator) lockServer(serverID uint) func() {
	return o.locks.lock(serverID)
}

// BuildServerConfig assembles a server's full official config.json from the DB.
func (o *Orchestrator) BuildServerConfig(srv *model.Server) ([]byte, error) {
	if srv.EffectiveConfigMode() == model.ConfigModeRaw {
		if !json.Valid(srv.RawConfig) {
			return nil, fmt.Errorf("server raw config is not valid JSON")
		}
		return append([]byte(nil), srv.RawConfig...), nil
	}
	var inbounds []model.Inbound
	if err := o.db.Where("server_id = ? AND enabled = ?", srv.ID, true).Find(&inbounds).Error; err != nil {
		return nil, fmt.Errorf("load inbounds for server %d: %w", srv.ID, err)
	}

	ins := make([]singbox.InboundInput, 0, len(inbounds))
	for _, ib := range inbounds {
		var st singbox.InboundSettings
		if len(ib.Settings) > 0 {
			if err := json.Unmarshal(ib.Settings, &st); err != nil {
				return nil, fmt.Errorf("inbound %q settings: %w", ib.Tag, err)
			}
		}
		var users []singbox.ProxyUser
		if st.UseMultiUser(string(ib.Type)) {
			st.SingleUser = false
			loadedUsers, loadErr := proxyUsersForInbound(o.db, &ib, st)
			if loadErr != nil {
				return nil, fmt.Errorf("load users for inbound %q: %w", ib.Tag, loadErr)
			}
			users = loadedUsers
		} else {
			st.MultiUser = false
			st.SingleUser = true
		}
		ins = append(ins, singbox.InboundInput{
			Tag:        ib.Tag,
			Type:       string(ib.Type),
			ListenPort: ib.ListenPort,
			Settings:   st,
			Users:      users,
		})
	}

	// Outbounds (direct + landing proxies).
	var obRows []model.Outbound
	if err := o.db.Where("server_id = ?", srv.ID).Order("sort").Find(&obRows).Error; err != nil {
		return nil, fmt.Errorf("load outbounds for server %d: %w", srv.ID, err)
	}
	obs := make([]singbox.OutboundInput, 0, len(obRows))
	for _, ob := range obRows {
		var os outboundStoredSettings
		if len(ob.Settings) > 0 {
			if err := json.Unmarshal(ob.Settings, &os); err != nil {
				return nil, fmt.Errorf("outbound %q settings: %w", ob.Tag, err)
			}
		}
		obs = append(obs, singbox.OutboundInput{
			Tag: ob.Tag, Type: ob.Type,
			Server: os.Server, ServerPort: os.ServerPort,
			Username: os.Username, UUID: os.UUID, Password: os.Password, Settings: os.Settings,
		})
	}

	// Route rules.
	var ruleRows []model.RouteRule
	if err := o.db.Where("server_id = ? AND enabled = ?", srv.ID, true).Order("sort").Find(&ruleRows).Error; err != nil {
		return nil, fmt.Errorf("load route rules for server %d: %w", srv.ID, err)
	}
	rules := make([]singbox.RuleInput, 0, len(ruleRows))
	for _, r := range ruleRows {
		var ri singbox.RuleInput
		if len(r.Match) > 0 {
			if err := json.Unmarshal(r.Match, &ri); err != nil {
				return nil, fmt.Errorf("route rule %d match: %w", r.ID, err)
			}
		}
		ri.Outbound = r.Outbound
		rules = append(rules, ri)
	}

	// Rule-sets.
	var rsRows []model.RuleSet
	if err := o.db.Where("server_id = ?", srv.ID).Find(&rsRows).Error; err != nil {
		return nil, fmt.Errorf("load rule sets for server %d: %w", srv.ID, err)
	}
	rsets := make([]singbox.RuleSetInput, 0, len(rsRows))
	for _, rs := range rsRows {
		rsets = append(rsets, singbox.RuleSetInput{
			Tag: rs.Tag, Type: rs.Type, URL: rs.URL, Path: rs.Path, Format: rs.Format,
			DownloadDetour: rs.DownloadDetour, UpdateInterval: rs.UpdateInterval,
		})
	}

	return singbox.BuildServerConfig(singbox.ServerConfigInput{
		Inbounds:        ins,
		Outbounds:       obs,
		Rules:           rules,
		RuleSets:        rsets,
		Final:           srv.FinalOutbound,
		StatsController: protocol.LocalTrafficAddress,
	})
}

// ApplyDesiredConfig synchronously attempts to activate the saved desired
// state. Offline nodes are pending because AfterRegister reconciles them on the
// next connection; all other errors are reported as failed instead of being
// hidden behind a successful database response.
func (o *Orchestrator) ApplyDesiredConfig(ctx context.Context, serverID uint) ConfigApplyResult {
	// Persist the fact that even an empty generated config is intentional. This
	// makes an offline delete of the final inbound/outbound reconcile correctly
	// after reconnect instead of being mistaken for a never-configured node.
	if err := o.db.Model(&model.Server{}).Where("id = ?", serverID).Update("config_initialized", true).Error; err != nil {
		return ConfigApplyResult{ApplyState: ConfigFailed, ApplyError: "mark desired config initialized: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	err := o.PushConfig(ctx, serverID)
	if err == nil {
		return ConfigApplyResult{ApplyState: ConfigApplied}
	}
	if errors.Is(err, ErrAgentOffline) {
		return ConfigApplyResult{ApplyState: ConfigPending}
	}
	return ConfigApplyResult{ApplyState: ConfigFailed, ApplyError: err.Error()}
}

// PushConfig builds and applies a server's config, blocking on the agent's ack.
func (o *Orchestrator) PushConfig(ctx context.Context, serverID uint) error {
	unlock := o.lockServer(serverID)
	defer unlock()
	return o.pushConfigUnlocked(ctx, serverID)
}

func (o *Orchestrator) pushConfigUnlocked(ctx context.Context, serverID uint) error {
	var srv model.Server
	if err := o.db.First(&srv, serverID).Error; err != nil {
		return err
	}
	raw, err := o.BuildServerConfig(&srv)
	if err != nil {
		return err
	}
	_, err = o.hub.SendCommand(ctx, serverID, protocol.CmdApplyConfig, protocol.ApplyConfigCmd{
		Config: raw,
		Reload: true,
	})
	return err
}

// ApplyRawConfig changes the desired source of truth and applies it as one
// serialized operation. If the Agent rejects the config, the previous database
// mode/snapshot is restored so reconnects cannot overwrite the running node with
// a half-saved edit.
func (o *Orchestrator) ApplyRawConfig(ctx context.Context, serverID uint, raw []byte) (protocol.CommandResultEvt, error) {
	unlock := o.lockServer(serverID)
	defer unlock()
	return o.applyRawConfigUnlocked(ctx, serverID, raw)
}

// applyRawConfigUnlocked applies and persists raw JSON while the caller owns
// the server configuration lock. The API uses it to keep the subsequent
// structured-view synchronization in the same serialized operation.
func (o *Orchestrator) applyRawConfigUnlocked(ctx context.Context, serverID uint, raw []byte) (protocol.CommandResultEvt, error) {
	var old model.Server
	if err := o.db.Select("id", "config_mode", "raw_config", "config_initialized").First(&old, serverID).Error; err != nil {
		return protocol.CommandResultEvt{}, err
	}
	if err := o.db.Model(&model.Server{}).Where("id = ?", serverID).Updates(map[string]any{
		"config_mode": model.ConfigModeRaw,
		"raw_config":  model.JSONText(append([]byte(nil), raw...)),
	}).Error; err != nil {
		return protocol.CommandResultEvt{}, err
	}
	res, err := o.hub.SendCommand(ctx, serverID, protocol.CmdApplyConfig, protocol.ApplyConfigCmd{
		Config: json.RawMessage(raw), Reload: true,
	})
	if err != nil {
		if rollbackErr := o.db.Model(&model.Server{}).Where("id = ?", serverID).Updates(map[string]any{
			"config_mode": old.ConfigMode,
			"raw_config":  old.RawConfig,
		}).Error; rollbackErr != nil {
			return res, fmt.Errorf("apply raw config: %v; restore raw snapshot: %w", err, rollbackErr)
		}
		return res, err
	}
	return res, nil
}

// SwitchToManagedConfig serializes the mode change with every other config
// apply. The database is changed before the generated config is built, and is
// restored if the Agent rejects or cannot receive it.
func (o *Orchestrator) SwitchToManagedConfig(ctx context.Context, serverID uint) error {
	unlock := o.lockServer(serverID)
	defer unlock()

	var old model.Server
	if err := o.db.Select("id", "config_mode", "raw_config", "config_initialized").First(&old, serverID).Error; err != nil {
		return err
	}
	if err := o.db.Model(&model.Server{}).Where("id = ?", serverID).Updates(map[string]any{
		"config_mode":        model.ConfigModeManaged,
		"config_initialized": true,
	}).Error; err != nil {
		return err
	}
	if err := o.pushConfigUnlocked(ctx, serverID); err != nil {
		if rollbackErr := o.db.Model(&model.Server{}).Where("id = ?", serverID).Updates(map[string]any{
			"config_mode":        old.ConfigMode,
			"config_initialized": old.ConfigInitialized,
		}).Error; rollbackErr != nil {
			return fmt.Errorf("push managed config: %v; restore config mode: %w", err, rollbackErr)
		}
		return err
	}
	return nil
}

// PushConfigAsync pushes in the background, ignoring offline agents.
func (o *Orchestrator) PushConfigAsync(serverID uint) {
	o.asyncMu.Lock()
	if o.asyncRunning[serverID] {
		o.asyncPending[serverID] = true
		o.asyncMu.Unlock()
		return
	}
	o.asyncRunning[serverID] = true
	o.asyncMu.Unlock()

	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			err := o.PushConfig(ctx, serverID)
			cancel()
			if err != nil && !errors.Is(err, ErrAgentOffline) {
				log.Printf("orchestrator: push config to server %d failed: %v", serverID, err)
			}

			o.asyncMu.Lock()
			if o.asyncPending[serverID] {
				delete(o.asyncPending, serverID)
				o.asyncMu.Unlock()
				continue
			}
			delete(o.asyncRunning, serverID)
			o.asyncMu.Unlock()
			return
		}
	}()
}

// PushConfigIfManaged reconciles nodes that have a raw config, structured
// records, or an explicitly initialized (possibly empty) managed config. A
// brand-new node remains untouched until the administrator first manages it.
func (o *Orchestrator) PushConfigIfManaged(serverID uint) {
	managed, err := o.serverIsManaged(serverID)
	if err != nil {
		log.Printf("orchestrator: inspect desired config for server %d: %v", serverID, err)
		return
	}
	if managed {
		o.PushConfigAsync(serverID)
	}
}

func (o *Orchestrator) serverIsManaged(serverID uint) (bool, error) {
	var srv model.Server
	if err := o.db.Select("config_mode", "raw_config", "config_initialized").First(&srv, serverID).Error; err != nil {
		return false, err
	}
	if srv.EffectiveConfigMode() == model.ConfigModeRaw || srv.ConfigInitialized {
		return true, nil
	}
	var n int64
	if err := o.db.Model(&model.Inbound{}).Where("server_id = ?", serverID).Count(&n).Error; err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	if err := o.db.Model(&model.Outbound{}).Where("server_id = ?", serverID).Count(&n).Error; err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	if err := o.db.Model(&model.RouteRule{}).Where("server_id = ?", serverID).Count(&n).Error; err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	if err := o.db.Model(&model.RuleSet{}).Where("server_id = ?", serverID).Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}
