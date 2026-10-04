package panel

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

type generatedStatsConfig struct {
	Route struct {
		Rules []map[string]any `json:"rules"`
	} `json:"route"`
	Services []struct {
		Type       string `json:"type"`
		Tag        string `json:"tag"`
		Listen     string `json:"listen"`
		ListenPort int    `json:"listen_port"`
		Secret     string `json:"secret"`
		Dashboard  any    `json:"dashboard"`
	} `json:"services"`
	Experimental struct {
		ClashAPI struct {
			ExternalController string `json:"external_controller"`
			Secret             string `json:"secret"`
		} `json:"clash_api"`
	} `json:"experimental"`
}

func buildManagedTestConfig(t *testing.T, srv *model.Server) generatedStatsConfig {
	t.Helper()
	db := testDB(t)
	if err := db.Create(srv).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := NewOrchestrator(db, NewHub(db)).BuildServerConfig(srv)
	if err != nil {
		t.Fatal(err)
	}
	var cfg generatedStatsConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestManagedConfigProtectsStatsEndpoints(t *testing.T) {
	cfg := buildManagedTestConfig(t, &model.Server{Name: "n", AgentToken: "token-a", SingboxVersion: "1.14.2"})
	secret := cfg.Experimental.ClashAPI.Secret
	if len(secret) != 64 || secret != statsSecret("token-a") {
		t.Fatalf("clash api secret = %q", secret)
	}
	if len(cfg.Services) != 1 {
		t.Fatalf("services = %+v", cfg.Services)
	}
	service := cfg.Services[0]
	if service.Type != "api" || service.Tag != singbox.StatsServiceTag || service.Listen != "127.0.0.1" ||
		service.ListenPort != 29092 || service.Secret != secret || service.Dashboard != nil {
		t.Fatalf("stats service = %+v", service)
	}
	if len(cfg.Route.Rules) == 0 || !singbox.IsLoopbackGuardRule(cfg.Route.Rules[0]) {
		t.Fatalf("first route rule must be the loopback guard: %v", cfg.Route.Rules)
	}
	if statsSecret("token-a") == statsSecret("token-b") || statsSecret("") != "" {
		t.Fatal("stats secret must be per node and empty without a token")
	}
}

func TestStatsAPIServiceVersionGate(t *testing.T) {
	cases := map[string]bool{
		"":                false,
		"1.13.21":         false,
		"1.14.0-beta.17":  false,
		"1.14.0-rc.5":     false,
		"1.14.0":          true,
		"v1.14.2":         true,
		"1.15.0-alpha.10": true,
	}
	for version, want := range cases {
		if got := statsAPISupported(version); got != want {
			t.Errorf("statsAPISupported(%q) = %v, want %v", version, got, want)
		}
		cfg := buildManagedTestConfig(t, &model.Server{Name: "n", AgentToken: "tok", SingboxVersion: version})
		if (len(cfg.Services) == 1) != want {
			t.Errorf("version %q: services = %+v", version, cfg.Services)
		}
		if cfg.Experimental.ClashAPI.Secret == "" {
			t.Errorf("version %q: clash api must always carry a secret", version)
		}
	}
}

func TestStatsAPIServiceSkippedOnPortConflict(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "n", AgentToken: "tok", SingboxVersion: "1.14.2"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{ServerID: srv.ID, Tag: "legacy", Type: "socks", ListenPort: 29092, Enabled: true}
	if err := db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := NewOrchestrator(db, NewHub(db)).BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	var cfg generatedStatsConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Services) != 0 {
		t.Fatalf("service must be skipped when an inbound owns its port: %+v", cfg.Services)
	}
	app := &App{db: db}
	if err := app.validateInboundIdentity(db, srv.ID, 0, "new", 29092); err == nil {
		t.Fatal("new inbounds must not use the stats API port")
	}
}

func TestImportKeepsPanelGeneratedStatsConfigManaged(t *testing.T) {
	raw, err := singbox.BuildServerConfig(singbox.ServerConfigInput{
		Inbounds: []singbox.InboundInput{{
			Tag: "Snell", Type: "snell", ListenPort: 38376,
			Settings: singbox.InboundSettings{SingleUser: true, SnellVersion: 5, SnellPSK: "secret"},
		}},
		Rules:           []singbox.RuleInput{{DomainSuffix: []string{"example.com"}, Outbound: "direct"}},
		StatsController: protocol.LocalTrafficAddress,
		StatsSecret:     statsSecret("tok"),
		StatsAPI:        protocol.LocalStatsAPIAddress,
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range parsed.Rules {
		if rule.Match.Action == "reject" {
			t.Fatalf("loopback guard leaked into imported rules: %+v", parsed.Rules)
		}
	}
	if mode := importedConfigMode(parsed, raw); mode != model.ConfigModeManaged {
		t.Fatalf("panel-generated config imported as %q", mode)
	}
}

func TestHeartbeatRepushesOnlyWhenStatsCapabilityFlips(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "n", AgentToken: "hb", SingboxVersion: "1.13.21"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	hub := NewHub(db)
	var pushes atomic.Int32
	hub.AfterStatsCapabilityChange = func(uint) { pushes.Add(1) }
	conn := hub.register(srv.ID, "", nil)
	for _, version := range []string{"1.13.21", "", "1.14.2", "1.14.2", "1.15.0-alpha.10", "1.15.0-alpha.10"} {
		hub.onHeartbeat(conn, protocol.HeartbeatEvt{SingboxVersion: version})
	}
	if got := pushes.Load(); got != 1 {
		t.Fatalf("pushes after crossing 1.14 = %d, want 1", got)
	}
	hub.onHeartbeat(conn, protocol.HeartbeatEvt{SingboxVersion: "1.14.0-beta.3"})
	if got := pushes.Load(); got != 2 {
		t.Fatalf("pushes after downgrade = %d, want 2", got)
	}
}

func TestRawConfigSecurityWarnings(t *testing.T) {
	open := []byte(`{"experimental":{"clash_api":{"external_controller":"127.0.0.1:9090"}},
		"services":[{"type":"api","tag":"x","listen":"127.0.0.1","listen_port":9091}]}`)
	if got := rawConfigSecurityWarnings(open); len(got) != 2 {
		t.Fatalf("warnings = %v", got)
	}
	protected := []byte(`{"experimental":{"clash_api":{"external_controller":"127.0.0.1:9090","secret":"s"}},
		"services":[{"type":"api","tag":"x","listen":"127.0.0.1","listen_port":9091,"secret":"s"}]}`)
	if got := rawConfigSecurityWarnings(protected); len(got) != 0 {
		t.Fatalf("warnings = %v", got)
	}
}
