package panel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

type stagedRoutingTracker struct {
	mu      sync.Mutex
	added   map[uint]map[uint]bool // consumerServerID -> sourceInboundID -> true
	removed map[uint]map[uint]bool // consumerServerID -> sourceInboundID -> true
}

func (t *stagedRoutingTracker) recordAdd(consumerServerID, sourceInboundID uint) {
	if consumerServerID == 0 || sourceInboundID == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.removed != nil && t.removed[consumerServerID][sourceInboundID] {
		delete(t.removed[consumerServerID], sourceInboundID)
		if len(t.removed[consumerServerID]) == 0 {
			delete(t.removed, consumerServerID)
		}
		return
	}
	if t.added == nil {
		t.added = make(map[uint]map[uint]bool)
	}
	if t.added[consumerServerID] == nil {
		t.added[consumerServerID] = make(map[uint]bool)
	}
	t.added[consumerServerID][sourceInboundID] = true
}

func (t *stagedRoutingTracker) recordRemove(db *gorm.DB, consumerServerID, sourceInboundID uint) {
	if consumerServerID == 0 || sourceInboundID == 0 {
		return
	}
	if db != nil {
		var remaining int64
		if err := db.Model(&model.Outbound{}).
			Where("server_id = ? AND source_kind = ? AND source_id = ?", consumerServerID, model.OutboundSourceInbound, sourceInboundID).
			Count(&remaining).Error; err == nil && remaining > 0 {
			return
		}
	}
	var applied bool
	if db != nil {
		var srv model.Server
		if err := db.Select("id", "config_initialized", "config_applied_revision").First(&srv, consumerServerID).Error; err == nil {
			applied = srv.ConfigInitialized || srv.ConfigAppliedRevision > 0
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.added != nil && t.added[consumerServerID][sourceInboundID] {
		delete(t.added[consumerServerID], sourceInboundID)
		if len(t.added[consumerServerID]) == 0 {
			delete(t.added, consumerServerID)
		}
		return
	}
	if !applied {
		return
	}
	if t.removed == nil {
		t.removed = make(map[uint]map[uint]bool)
	}
	if t.removed[consumerServerID] == nil {
		t.removed[consumerServerID] = make(map[uint]bool)
	}
	t.removed[consumerServerID][sourceInboundID] = true
}

func (t *stagedRoutingTracker) activeRetainedConsumers(db *gorm.DB, inboundIDs []uint) map[uint][]uint {
	if len(inboundIDs) == 0 || db == nil {
		return nil
	}
	wanted := make(map[uint]bool, len(inboundIDs))
	for _, id := range inboundIDs {
		wanted[id] = true
	}
	t.mu.Lock()
	var candidateConsumers []uint
	byConsumer := make(map[uint][]uint)
	for consumerID, sources := range t.removed {
		for inboundID := range sources {
			if wanted[inboundID] {
				byConsumer[consumerID] = append(byConsumer[consumerID], inboundID)
			}
		}
		if len(byConsumer[consumerID]) > 0 {
			candidateConsumers = append(candidateConsumers, consumerID)
		}
	}
	t.mu.Unlock()
	if len(candidateConsumers) == 0 {
		return nil
	}
	var servers []model.Server
	if err := db.Select("id", "config_mode", "raw_config", "config_revision", "config_applied_revision").
		Where("id IN ?", candidateConsumers).Find(&servers).Error; err != nil {
		return nil
	}
	dirtyConsumers := make(map[uint]bool, len(servers))
	var cleanConsumers []uint
	for i := range servers {
		if servers[i].EffectiveConfigMode() == model.ConfigModeManaged && servers[i].ConfigRevision > servers[i].ConfigAppliedRevision {
			dirtyConsumers[servers[i].ID] = true
		} else {
			cleanConsumers = append(cleanConsumers, servers[i].ID)
		}
	}
	if len(cleanConsumers) > 0 {
		t.mu.Lock()
		for _, consumerID := range cleanConsumers {
			delete(t.removed, consumerID)
			delete(t.added, consumerID)
		}
		t.mu.Unlock()
	}
	out := make(map[uint][]uint)
	for consumerID, inbounds := range byConsumer {
		if !dirtyConsumers[consumerID] {
			continue
		}
		for _, inboundID := range inbounds {
			out[inboundID] = append(out[inboundID], consumerID)
		}
	}
	return out
}

func (t *stagedRoutingTracker) popApplied(consumerServerID uint) []uint {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.added != nil {
		delete(t.added, consumerServerID)
	}
	if t.removed == nil {
		return nil
	}
	sources := t.removed[consumerServerID]
	delete(t.removed, consumerServerID)
	if len(sources) == 0 {
		return nil
	}
	out := make([]uint, 0, len(sources))
	for inboundID := range sources {
		out = append(out, inboundID)
	}
	return out
}

// Orchestrator builds server configs from the database and pushes them to agents.
type Orchestrator struct {
	db    *gorm.DB
	hub   *Hub
	locks *keyedMutex[uint]
	// app resolves node-imported outbounds against their live source. Set by
	// NewApp; nil in tests that build an orchestrator directly, where such
	// outbounds keep their imported snapshot.
	app *App

	ctx           context.Context
	asyncMu       sync.Mutex
	asyncRunning  map[uint]bool
	asyncPending  map[uint]bool
	stagedRouting stagedRoutingTracker
}

func (o *Orchestrator) routingTracker() *stagedRoutingTracker {
	if o.app != nil && o.app.orch != nil {
		return &o.app.orch.stagedRouting
	}
	return &o.stagedRouting
}

var errDryRunRollback = errors.New("dry-run rollback")

// app returns the panel that owns this orchestrator, or a bare App backed by
// the same database when tests construct one without a panel.
func (o *Orchestrator) appOrFallback() *App {
	if o.app != nil {
		return o.app
	}
	return &App{db: o.db, orch: o}
}

type ConfigApplyState string

const (
	ConfigApplied       ConfigApplyState = "applied"
	ConfigPending       ConfigApplyState = "pending"
	ConfigFailed        ConfigApplyState = "failed"
	ConfigStaged        ConfigApplyState = "staged"
	asyncPushAttempts                    = 3
	asyncPushRetryDelay                  = 2 * time.Second
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
	return o.buildServerConfig(srv, nil)
}

func (o *Orchestrator) buildServerConfig(srv *model.Server, projection *singbox.ConfigProjection) ([]byte, error) {
	return o.buildServerConfigWithMode(srv, projection, true)
}

func (o *Orchestrator) buildServerConfigReadOnly(srv *model.Server, projection *singbox.ConfigProjection) ([]byte, error) {
	return o.buildServerConfigWithMode(srv, projection, false)
}

func (o *Orchestrator) buildServerConfigWithMode(srv *model.Server, projection *singbox.ConfigProjection, persistIdentities bool) ([]byte, error) {
	if srv.EffectiveConfigMode() == model.ConfigModeRaw {
		if !json.Valid(srv.RawConfig) {
			return nil, fmt.Errorf("server raw config is not valid JSON")
		}
		return append([]byte(nil), srv.RawConfig...), nil
	}
	var result []byte
	entered := false
	err := o.db.Transaction(func(db *gorm.DB) error {
		entered = true
		var inbounds []model.Inbound
		if err := db.Where("server_id = ? AND enabled = ?", srv.ID, true).Find(&inbounds).Error; err != nil {
			return fmt.Errorf("load inbounds for server %d: %w", srv.ID, err)
		}

		var (
			multiInboundIDs  []uint
			parsedInSettings = make([]singbox.InboundSettings, len(inbounds))
		)
		for i, ib := range inbounds {
			var st singbox.InboundSettings
			if len(ib.Settings) > 0 {
				if err := json.Unmarshal(ib.Settings, &st); err != nil {
					return fmt.Errorf("inbound %q settings: %w", ib.Tag, err)
				}
			}
			if err := st.ValidateMultiUser(string(ib.Type)); err != nil {
				return fmt.Errorf("inbound %q settings: %w", ib.Tag, err)
			}
			if err := resolveInboundListen(srv, &st); err != nil {
				return fmt.Errorf("inbound %q: %w", ib.Tag, err)
			}
			parsedInSettings[i] = st
			if st.UseMultiUser(string(ib.Type)) {
				multiInboundIDs = append(multiInboundIDs, ib.ID)
			}
		}

		var (
			enabledUsers    []model.User
			forwardsByIn    = make(map[uint][]model.UserForward)
			consumersByIn   = make(map[uint][]uint)
		)
		if len(multiInboundIDs) > 0 {
			if err := db.Where("enabled = ?", true).Order("id").Find(&enabledUsers).Error; err != nil {
				return fmt.Errorf("load users for server %d: %w", srv.ID, err)
			}
			var forwards []model.UserForward
			if err := db.Where("server_id = ? AND inbound_id IN ? AND user_id IN (?)",
				srv.ID, multiInboundIDs,
				db.Model(&model.User{}).Select("id").Where("forwarding_enabled = ?", true),
			).Order("id").Find(&forwards).Error; err != nil {
				return fmt.Errorf("load forwards for server %d: %w", srv.ID, err)
			}
			for _, f := range forwards {
				forwardsByIn[f.InboundID] = append(forwardsByIn[f.InboundID], f)
			}
			var consumerRows []struct {
				SourceID uint
				ServerID uint
			}
			if err := db.Model(&model.Outbound{}).
				Select("DISTINCT source_id, server_id").
				Where("source_kind = ? AND source_id IN ?", model.OutboundSourceInbound, multiInboundIDs).
				Scan(&consumerRows).Error; err != nil {
				return fmt.Errorf("load routing users for server %d: %w", srv.ID, err)
			}
			for _, cr := range consumerRows {
				consumersByIn[cr.SourceID] = append(consumersByIn[cr.SourceID], cr.ServerID)
			}
			for inboundID, retainedConsumers := range o.routingTracker().activeRetainedConsumers(db, multiInboundIDs) {
				consumersByIn[inboundID] = append(consumersByIn[inboundID], retainedConsumers...)
			}
		}

		ins := make([]singbox.InboundInput, 0, len(inbounds))
		for i, ib := range inbounds {
			st := parsedInSettings[i]
			var users []singbox.ProxyUser
			if st.UseMultiUser(string(ib.Type)) {
				st.SingleUser = false
				loadedUsers, loadErr := resolveProxyUsersForInbound(db, &ib, st, enabledUsers, forwardsByIn[ib.ID], true, persistIdentities)
				if loadErr != nil {
					return fmt.Errorf("load users for inbound %q: %w", ib.Tag, loadErr)
				}
				users = loadedUsers
				// Other servers may route through this inbound; each consumer needs
				// its own dedicated credential in this server's config.
				routingUsers, routeErr := routingIdentitiesFromConsumers(srv, ib.ID, consumersByIn[ib.ID])
				if routeErr != nil {
					return fmt.Errorf("load routing users for inbound %q: %w", ib.Tag, routeErr)
				}
				users = append(users, routingUsers...)
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

		// Route rules are loaded before outbounds because a user-private exit only
		// belongs in this server's config when one of its rules references it.
		var ruleRows []model.RouteRule
		if err := db.Where("server_id = ? AND enabled = ?", srv.ID, true).Order("sort, id").Find(&ruleRows).Error; err != nil {
			return fmt.Errorf("load route rules for server %d: %w", srv.ID, err)
		}
		ruleRows, err := materializeForwardRoutes(db, srv.ID, inbounds, ruleRows)
		if err != nil {
			return fmt.Errorf("load forward routes for server %d: %w", srv.ID, err)
		}
		privateTags, err := privateOutboundTagsForRules(db, ruleRows)
		if err != nil {
			return fmt.Errorf("load private outbounds for server %d: %w", srv.ID, err)
		}

		// Outbounds: shared server rows plus the private exits actually referenced
		// by this server. Private credentials stay global so one user exit can serve
		// forwards from multiple entry servers without duplicating the secret.
		var obRows []model.Outbound
		query := db.Where("server_id = ? AND owner_user_id = 0", srv.ID)
		if len(privateTags) > 0 {
			query = db.Where("(server_id = ? AND owner_user_id = 0) OR (server_id = 0 AND owner_user_id <> 0 AND tag IN ?)", srv.ID, privateTags)
		}
		if err := query.Order("sort").Find(&obRows).Error; err != nil {
			return fmt.Errorf("load outbounds for server %d: %w", srv.ID, err)
		}
		obs := make([]singbox.OutboundInput, 0, len(obRows))
		privateByTag := make(map[string]model.Outbound)
		seenTags := make(map[string]bool, len(obRows))
		for _, ob := range obRows {
			if seenTags[ob.Tag] {
				return fmt.Errorf("outbound tag %q is defined by both a shared and a private outbound", ob.Tag)
			}
			seenTags[ob.Tag] = true
			if ob.OwnerUserID != 0 {
				privateByTag[ob.Tag] = ob
			}
			var os outboundStoredSettings
			if len(ob.Settings) > 0 {
				if err := json.Unmarshal(ob.Settings, &os); err != nil {
					return fmt.Errorf("outbound %q settings: %w", ob.Tag, err)
				}
			}
			// Node-imported outbounds follow their source, so edits there apply
			// without re-importing. A source that became unusable falls back to the
			// snapshot taken at import time: a stale route still beats dropping the
			// outbound (which every rule referencing it would then lose).
			if ob.Referenced() {
				if client, err := o.appOrFallback().outboundSourceClientWithDB(db, &ob, srv.ID); err == nil {
					os = outboundStoredSettings{
						Server: client.Server, ServerPort: client.ServerPort,
						UUID: client.User.UUID, Username: client.User.Username, Password: client.User.Password,
						Settings: client.Settings,
					}
					ob.Type = client.Type
				} else {
					if errors.Is(err, errNodeAddress) {
						return fmt.Errorf("outbound %q: %w", ob.Tag, err)
					}
					log.Printf("orchestrator: outbound %q on server %d keeps its imported snapshot: %v", ob.Tag, srv.ID, err)
				}
			}
			obs = append(obs, singbox.OutboundInput{
				Tag: ob.Tag, Type: ob.Type,
				Server: os.Server, ServerPort: os.ServerPort,
				Username: os.Username, UUID: os.UUID, Password: os.Password, Settings: os.Settings,
			})
		}

		rules := make([]singbox.RuleInput, 0, len(ruleRows))
		for _, r := range ruleRows {
			if !builtinOutboundTags[r.Outbound] && !seenTags[r.Outbound] && !singbox.OverlayHasOutbound(srv.ConfigOverlay, r.Outbound) {
				return fmt.Errorf("route rule %d references missing outbound %q", r.ID, r.Outbound)
			}
			var ri singbox.RuleInput
			if len(r.Match) > 0 {
				if err := json.Unmarshal(r.Match, &ri); err != nil {
					return fmt.Errorf("route rule %d match: %w", r.ID, err)
				}
			}
			if outbound, private := privateByTag[r.Outbound]; private {
				if err := validatePrivateRouteScope(db, srv.ID, outbound, ri); err != nil {
					return err
				}
			}
			ri.Outbound = r.Outbound
			rules = append(rules, ri)
		}
		if srv.FinalOutbound != "" && srv.FinalOutbound != "direct" && !seenTags[srv.FinalOutbound] && !singbox.OverlayHasOutbound(srv.ConfigOverlay, srv.FinalOutbound) {
			return fmt.Errorf("final references missing outbound %q", srv.FinalOutbound)
		}

		// Rule-sets.
		var rsRows []struct {
			ID             uint
			Tag            string
			Type           string
			URL            string
			Path           string
			Format         string
			DownloadDetour string
			UpdateInterval string
		}
		if err := db.Table("server_rule_sets").
			Select("rule_sets.id, rule_sets.tag, rule_sets.type, rule_sets.url, rule_sets.path, rule_sets.format, server_rule_sets.download_detour, rule_sets.update_interval").
			Joins("JOIN rule_sets ON rule_sets.id = server_rule_sets.rule_set_id").
			Where("server_rule_sets.server_id = ?", srv.ID).
			Order("rule_sets.id").
			Scan(&rsRows).Error; err != nil {
			return fmt.Errorf("load rule sets for server %d: %w", srv.ID, err)
		}
		rsets := make([]singbox.RuleSetInput, 0, len(rsRows))
		for _, rs := range rsRows {
			rsets = append(rsets, singbox.RuleSetInput{
				Tag: rs.Tag, Type: rs.Type, URL: rs.URL, Path: rs.Path, Format: rs.Format,
				DownloadDetour: rs.DownloadDetour, UpdateInterval: rs.UpdateInterval,
			})
		}

		input := singbox.ServerConfigInput{
			LogLevel:        srv.LogLevel,
			Inbounds:        ins,
			Outbounds:       obs,
			Rules:           rules,
			RuleSets:        rsets,
			Final:           srv.FinalOutbound,
			StatsController: protocol.LocalTrafficAddress,
			StatsSecret:     statsSecret(srv.AgentToken),
			StatsAPI:        statsAPIAddress(srv, inbounds),
		}
		if singbox.HasConfigOverlay(srv.ConfigOverlay) {
			input.ProjectionOnly = true
			var overlay singbox.ConfigOverlay
			if err := json.Unmarshal(srv.ConfigOverlay, &overlay); err != nil {
				return fmt.Errorf("decode preserved config: %w", err)
			}
			// Imported configurations keep their existing control endpoints. Do not
			// replace a custom service or claim ports the original did not use.
			input.StatsController = overlay.Baseline.Input.StatsController
			input.StatsAPI = overlay.Baseline.Input.StatsAPI
			input.StatsSecret = overlay.Baseline.Input.StatsSecret
		}
		p := singbox.ConfigProjection{Input: input, IDs: make(map[string][]string)}
		for _, row := range inbounds {
			p.IDs["inbounds"] = append(p.IDs["inbounds"], fmt.Sprint(row.ID))
		}
		for _, row := range obRows {
			if row.OwnerUserID != 0 {
				p.IDs["outbounds"] = append(p.IDs["outbounds"], fmt.Sprintf("private:%d:%d:%s", row.OwnerUserID, row.ID, row.Tag))
			} else {
				p.IDs["outbounds"] = append(p.IDs["outbounds"], fmt.Sprint(row.ID))
			}
		}
		for _, row := range ruleRows {
			p.IDs["rules"] = append(p.IDs["rules"], configRuleIdentity(row))
			p.RuleTargets = append(p.RuleTargets, row.Outbound)
		}
		for _, row := range rsRows {
			p.IDs["rule_set"] = append(p.IDs["rule_set"], fmt.Sprint(row.ID))
		}
		if projection != nil {
			*projection = p
		}
		rendered, err := singbox.BuildServerConfig(input)
		if err != nil {
			return err
		}
		if singbox.HasConfigOverlay(srv.ConfigOverlay) && projection == nil {
			merged, err := singbox.ApplyConfigOverlay(srv.ConfigOverlay, p)
			if err != nil {
				return err
			}
			parsed, err := singbox.ParseServerConfig(merged)
			if err != nil {
				return err
			}
			// Unknown conditions (for example invert or logical subrules) can
			// change the scope of a route whose form fields look user-specific.
			// Validate the actual merged file before it can expose a private exit.
			if err := validateImportedPrivateRouting(db, srv.ID, parsed, merged); err != nil {
				return err
			}
			result = merged
			return nil
		}
		// Restore the fields this server's config uses but the panel cannot model,
		// so a partially understood config stays editable instead of locking the UI.
		if residual := decodedResidual(srv); !residual.Empty() {
			rendered, err = singbox.ApplyResidual(rendered, residual)
			if err != nil {
				return err
			}
		}
		result = rendered
		return nil
	})
	if err != nil {
		if !entered {
			return nil, fmt.Errorf("load inbounds for server %d: %w", srv.ID, err)
		}
		return nil, err
	}
	return result, nil
}

// decodedResidual returns the preserved fields the panel cannot model. A
// malformed or absent blob yields an empty residual, so the generated config is
// pushed as-is rather than failing the push.
func decodedResidual(srv *model.Server) singbox.Residual {
	if len(srv.ConfigResidual) == 0 {
		return singbox.Residual{}
	}
	var residual singbox.Residual
	if json.Unmarshal(srv.ConfigResidual, &residual) != nil {
		return singbox.Residual{}
	}
	return residual
}

// privateOutboundTagsForRules returns private exit tags referenced by this
// server's enabled rules. Private exits are stored once (server_id = 0), so
// config assembly must include them on demand rather than own a row per server.
func privateOutboundTagsForRules(db *gorm.DB, rules []model.RouteRule) ([]string, error) {
	candidates := make([]string, 0, len(rules))
	seen := make(map[string]bool, len(rules))
	for _, rule := range rules {
		if rule.Outbound == "" || seen[rule.Outbound] || builtinOutboundTags[rule.Outbound] {
			continue
		}
		seen[rule.Outbound] = true
		candidates = append(candidates, rule.Outbound)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	var tags []string
	if err := db.Model(&model.Outbound{}).
		Where("server_id = 0 AND owner_user_id <> 0 AND tag IN ?", candidates).
		Pluck("tag", &tags).Error; err != nil {
		return nil, err
	}
	return tags, nil
}

// statsSecret derives the per-node secret protecting the loopback stats
// endpoints. It is deterministic so repeated pushes produce identical configs
// (no needless sing-box restarts), and only the Agent (which reads it from the
// node's config) and the panel can know it.
func statsSecret(agentToken string) string {
	if agentToken == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(agentToken))
	mac.Write([]byte("singbox-panel stats"))
	return hex.EncodeToString(mac.Sum(nil))
}

// statsAPIAddress enables the sing-box API service only when the node runs a
// release that has it (1.14.0+), the node has a secret, and no enabled
// inbound already listens on the service port. Otherwise the Agent keeps the
// Clash-only (per-port) accounting.
func statsAPIAddress(srv *model.Server, inbounds []model.Inbound) string {
	if !statsAPISupported(srv.SingboxVersion) || srv.AgentToken == "" {
		return ""
	}
	port := addressPort(protocol.LocalStatsAPIAddress)
	for _, inbound := range inbounds {
		if inbound.ListenPort == port {
			return ""
		}
	}
	return protocol.LocalStatsAPIAddress
}

// statsAPISupported reports whether a sing-box version string is a release
// with the API service (1.14.0 and later; 1.14 pre-releases are excluded).
func statsAPISupported(version string) bool {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" {
		return false
	}
	return compareSemver(version, "1.14.0") >= 0
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
	o.syncCleanSourceServersBeforeApply(ctx, serverID)
	err := o.PushConfigWithTimeout(ctx, serverID, 55*time.Second)
	if err == nil {
		return ConfigApplyResult{ApplyState: ConfigApplied}
	}
	if errors.Is(err, ErrAgentOffline) {
		return ConfigApplyResult{ApplyState: ConfigPending}
	}
	return ConfigApplyResult{ApplyState: ConfigFailed, ApplyError: err.Error()}
}

func (o *Orchestrator) syncCleanSourceServersBeforeApply(ctx context.Context, consumerServerID uint) {
	var inboundIDs []uint
	if err := o.db.Model(&model.Outbound{}).
		Where("server_id = ? AND source_kind = ?", consumerServerID, model.OutboundSourceInbound).
		Distinct().Pluck("source_id", &inboundIDs).Error; err != nil || len(inboundIDs) == 0 {
		return
	}
	var sourceServerIDs []uint
	if err := o.db.Model(&model.Inbound{}).
		Where("id IN ?", inboundIDs).
		Distinct().Pluck("server_id", &sourceServerIDs).Error; err != nil || len(sourceServerIDs) == 0 {
		return
	}
	for _, sourceServerID := range sourceServerIDs {
		if sourceServerID == 0 || sourceServerID == consumerServerID {
			continue
		}
		var sourceSrv model.Server
		if err := o.db.Select("id", "config_mode", "raw_config", "config_revision", "config_applied_revision").
			First(&sourceSrv, sourceServerID).Error; err != nil {
			continue
		}
		// Never auto-push a source server that has unapplied staged edits.
		if sourceSrv.EffectiveConfigMode() != model.ConfigModeManaged || sourceSrv.ConfigRevision > sourceSrv.ConfigAppliedRevision {
			continue
		}
		if err := o.PushConfigWithTimeout(ctx, sourceServerID, 15*time.Second); err != nil && !errors.Is(err, ErrAgentOffline) {
			log.Printf("orchestrator: pre-sync routing source server %d for consumer %d: %v", sourceServerID, consumerServerID, err)
		}
	}
}

func (o *Orchestrator) finishConsumerRoutingSync(consumerServerID uint) {
	removedInboundIDs := o.routingTracker().popApplied(consumerServerID)
	if len(removedInboundIDs) == 0 {
		return
	}
	var sourceServerIDs []uint
	if err := o.db.Model(&model.Inbound{}).
		Where("id IN ?", removedInboundIDs).
		Distinct().Pluck("server_id", &sourceServerIDs).Error; err != nil || len(sourceServerIDs) == 0 {
		return
	}
	for _, sourceServerID := range sourceServerIDs {
		if sourceServerID == 0 || sourceServerID == consumerServerID {
			continue
		}
		var sourceSrv model.Server
		if err := o.db.Select("id", "config_mode", "raw_config", "config_revision", "config_applied_revision").
			First(&sourceSrv, sourceServerID).Error; err != nil {
			continue
		}
		if sourceSrv.EffectiveConfigMode() != model.ConfigModeManaged || sourceSrv.ConfigRevision > sourceSrv.ConfigAppliedRevision {
			continue
		}
		o.PushConfigIfManaged(sourceServerID)
	}
}

// PushConfig builds and applies a server's config, blocking on the agent's ack.
func (o *Orchestrator) PushConfig(ctx context.Context, serverID uint) error {
	unlock := o.lockServer(serverID)
	defer unlock()
	return o.pushConfigUnlocked(ctx, serverID)
}

// PushConfigWithTimeout starts the command timeout only after the per-server
// configuration lock has been acquired. Waiting behind another apply must not
// consume the entire delivery window before this operation can start.
func (o *Orchestrator) PushConfigWithTimeout(ctx context.Context, serverID uint, timeout time.Duration) error {
	unlock := o.lockServer(serverID)
	defer unlock()
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return o.pushConfigUnlocked(commandCtx, serverID)
}

func (o *Orchestrator) pushConfigUnlocked(ctx context.Context, serverID uint) error {
	var srv model.Server
	if err := o.db.First(&srv, serverID).Error; err != nil {
		return err
	}
	return o.pushConfigSnapshotUnlocked(ctx, &srv)
}

// pushConfigSnapshotUnlocked builds and pushes the current server configuration
// snapshot, then advances proxy-sync and configuration-revision markers once the
// Agent acknowledges the apply command.
func (o *Orchestrator) pushConfigSnapshotUnlocked(ctx context.Context, srv *model.Server) error {
	serverID := srv.ID
	raw, err := o.buildServerConfig(srv, nil)
	if err == nil {
		var command protocol.ApplyConfigCmd
		command, err = o.applyConfigCommand(raw)
		if err == nil {
			_, err = o.hub.SendCommand(ctx, serverID, protocol.CmdApplyConfig, command)
		}
	}
	if srv.ProxySyncRevision > srv.ProxySyncAppliedRevision {
		updates := map[string]any{"proxy_sync_failed": err != nil && !errors.Is(err, ErrAgentOffline)}
		if err == nil {
			updates["proxy_sync_applied_revision"] = srv.ProxySyncRevision
		}
		// A newer reset may commit while this config is in flight. Its revision
		// must remain pending even when this older command succeeds.
		if saveErr := o.db.Model(&model.Server{}).
			Where("id = ? AND proxy_sync_revision = ?", serverID, srv.ProxySyncRevision).
			Updates(updates).Error; saveErr != nil {
			return errors.Join(err, fmt.Errorf("record credential synchronization: %w", saveErr))
		}
	}
	if err == nil && (srv.ConfigRevision > srv.ConfigAppliedRevision || !srv.ConfigInitialized) {
		updates := map[string]any{"config_initialized": true}
		if srv.ConfigRevision > srv.ConfigAppliedRevision {
			updates["config_applied_revision"] = srv.ConfigRevision
		}
		if saveErr := o.db.Model(&model.Server{}).
			Where("id = ? AND config_revision = ?", serverID, srv.ConfigRevision).
			Updates(updates).Error; saveErr != nil {
			return fmt.Errorf("record applied configuration revision: %w", saveErr)
		}
	}
	if err == nil {
		o.finishConsumerRoutingSync(serverID)
	}
	return err
}

// applyRawConfigUnlocked applies and persists raw JSON while the caller owns
// the server configuration lock. The API uses it to keep the subsequent
// structured-view synchronization in the same serialized operation.
func (o *Orchestrator) applyRawConfigUnlocked(ctx context.Context, serverID uint, raw []byte) (protocol.CommandResultEvt, error) {
	command, err := o.applyConfigCommand(raw)
	if err != nil {
		return protocol.CommandResultEvt{}, err
	}
	var existing model.Server
	if err := o.db.Select("id").First(&existing, serverID).Error; err != nil {
		return protocol.CommandResultEvt{}, err
	}
	// Apply on the Agent before flipping config_mode to raw so any concurrent
	// EvtTraffic arriving while the previous managed config is still active does
	// not drop per-user traffic deltas.
	res, err := o.hub.SendCommand(ctx, serverID, protocol.CmdApplyConfig, command)
	if err != nil {
		return res, err
	}
	if err := o.db.Model(&model.Server{}).Where("id = ?", serverID).Updates(map[string]any{
		"config_mode": model.ConfigModeRaw,
		"raw_config":  model.JSONText(append([]byte(nil), raw...)),
		// The residual belongs to the previous managed view; raw mode re-applies
		// this JSON verbatim, so leaving a stale patch would be misleading.
		"config_residual":         model.JSONText(nil),
		"config_overlay":          model.JSONText(nil),
		"config_applied_revision": gorm.Expr("config_revision"),
	}).Error; err != nil {
		return res, err
	}
	o.finishConsumerRoutingSync(serverID)
	return res, nil
}

func (o *Orchestrator) applyConfigCommand(raw []byte) (protocol.ApplyConfigCmd, error) {
	if len(raw) > maxPanelAgentPayloadSize {
		return protocol.ApplyConfigCmd{}, fmt.Errorf("生成的配置体积（%d 字节）超过下发上限（%d 字节），请精简配置后再试", len(raw), maxPanelAgentPayloadSize)
	}
	command := protocol.ApplyConfigCmd{Config: json.RawMessage(raw)}
	var config struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return command, err
	}
	var tags []string
	for _, outbound := range config.Outbounds {
		tags = append(tags, outbound.Tag)
	}
	if len(tags) > 0 {
		if err := o.db.Model(&model.Outbound{}).Where("owner_user_id <> 0 AND tag IN ?", tags).Order("tag").Pluck("tag", &command.RestrictedOutboundTags).Error; err != nil {
			return command, err
		}
	}
	return command, nil
}

// SwitchToManagedConfig serializes the mode change with every other config
// apply. The database is changed before the generated config is built, and is
// restored if the Agent rejects or cannot receive it.
func (o *Orchestrator) SwitchToManagedConfig(ctx context.Context, serverID uint) error {
	unlock := o.lockServer(serverID)
	defer unlock()

	var old model.Server
	if err := o.db.First(&old, serverID).Error; err != nil {
		return err
	}
	app := o.appOrFallback()
	var parsed *singbox.ParsedConfig
	if old.EffectiveConfigMode() == model.ConfigModeRaw {
		var err error
		parsed, err = singbox.ParseServerConfig(old.RawConfig)
		if err != nil {
			return err
		}
	}

	var command protocol.ApplyConfigCmd
	if parsed != nil {
		app.userAccessMu.Lock()
	}
	dryErr := o.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if parsed != nil {
			if _, importErr := app.applyImportTx(tx, &old, parsed, old.RawConfig); importErr != nil {
				return importErr
			}
		} else {
			if err := tx.Model(&model.Server{}).Where("id = ?", serverID).Updates(map[string]any{
				"config_mode":        model.ConfigModeManaged,
				"config_initialized": true,
			}).Error; err != nil {
				return err
			}
		}
		var staged model.Server
		if err := tx.First(&staged, serverID).Error; err != nil {
			return err
		}
		dryOrch := &Orchestrator{db: tx, hub: o.hub, app: app}
		raw, err := dryOrch.buildServerConfigReadOnly(&staged, nil)
		if err != nil {
			return err
		}
		command, err = dryOrch.applyConfigCommand(raw)
		if err != nil {
			return err
		}
		return errDryRunRollback
	})
	if parsed != nil {
		app.userAccessMu.Unlock()
	}
	if dryErr != nil && !errors.Is(dryErr, errDryRunRollback) {
		return dryErr
	}

	if _, err := o.hub.SendCommand(ctx, serverID, protocol.CmdApplyConfig, command); err != nil {
		if old.ProxySyncRevision > old.ProxySyncAppliedRevision && !errors.Is(err, ErrAgentOffline) {
			_ = o.db.Model(&model.Server{}).
				Where("id = ? AND proxy_sync_revision = ?", serverID, old.ProxySyncRevision).
				Update("proxy_sync_failed", true).Error
		}
		return err
	}

	if parsed != nil {
		if err := app.applyImportUnlocked(&old, parsed, old.RawConfig); err != nil {
			return err
		}
	} else {
		if err := o.db.Model(&model.Server{}).Where("id = ?", serverID).Updates(map[string]any{
			"config_mode":             model.ConfigModeManaged,
			"config_initialized":      true,
			"config_applied_revision": gorm.Expr("config_revision"),
		}).Error; err != nil {
			return err
		}
	}
	if old.ProxySyncRevision > old.ProxySyncAppliedRevision {
		if saveErr := o.db.Model(&model.Server{}).
			Where("id = ? AND proxy_sync_revision = ?", serverID, old.ProxySyncRevision).
			Updates(map[string]any{
				"proxy_sync_failed":           false,
				"proxy_sync_applied_revision": old.ProxySyncRevision,
			}).Error; saveErr != nil {
			return fmt.Errorf("record credential synchronization: %w", saveErr)
		}
	}
	o.finishConsumerRoutingSync(serverID)
	return nil
}

// SetShutdownContext binds the orchestrator to a lifecycle context that cancels
// ongoing and retried background operations when the panel shuts down.
func (o *Orchestrator) SetShutdownContext(ctx context.Context) {
	o.asyncMu.Lock()
	defer o.asyncMu.Unlock()
	o.ctx = ctx
}

func (o *Orchestrator) shutdownCtx() context.Context {
	o.asyncMu.Lock()
	defer o.asyncMu.Unlock()
	if o.ctx != nil {
		return o.ctx
	}
	return context.Background()
}

// PushConfigAsync pushes in the background, ignoring offline agents.
func (o *Orchestrator) PushConfigAsync(serverID uint) {
	ctx := o.shutdownCtx()
	if ctx.Err() != nil {
		return
	}

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
			if ctx.Err() != nil {
				o.asyncMu.Lock()
				delete(o.asyncRunning, serverID)
				delete(o.asyncPending, serverID)
				o.asyncMu.Unlock()
				return
			}
			var err error
			for attempt := 1; attempt <= asyncPushAttempts; attempt++ {
				if ctx.Err() != nil {
					break
				}
				err = o.PushConfigWithTimeout(ctx, serverID, 60*time.Second)
				if err == nil || errors.Is(err, ErrAgentOffline) || errors.Is(err, context.Canceled) {
					break
				}
				log.Printf("orchestrator: push config to server %d failed (attempt %d/%d): %v", serverID, attempt, asyncPushAttempts, err)
				if attempt < asyncPushAttempts {
					select {
					case <-ctx.Done():
						break
					case <-time.After(asyncPushRetryDelay):
					}
				}
			}
			if err != nil && !errors.Is(err, ErrAgentOffline) && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
				log.Printf("orchestrator: push config to server %d exhausted retries: %v", serverID, err)
			}

			o.asyncMu.Lock()
			if ctx.Err() != nil {
				delete(o.asyncRunning, serverID)
				delete(o.asyncPending, serverID)
				o.asyncMu.Unlock()
				return
			}
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
	if !managed {
		_ = o.db.Model(&model.Server{}).
			Where("id = ? AND proxy_sync_revision > proxy_sync_applied_revision", serverID).
			Updates(map[string]any{
				"proxy_sync_failed":           false,
				"proxy_sync_applied_revision": gorm.Expr("proxy_sync_revision"),
			}).Error
		return
	}
	// Explicit administrator edits use stageConfigAndRespond and are never
	// published by this background path. Everything else queued here (credential
	// rotation, expiry, address changes, shared rule-set updates) is intended to
	// converge immediately, even when an unrelated staged edit is pending.
	o.PushConfigAsync(serverID)
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
	if err := o.db.Model(&model.ServerRuleSet{}).Where("server_id = ?", serverID).Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}
