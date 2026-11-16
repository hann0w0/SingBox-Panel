package panel

import (
	"encoding/json"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

// A config using fields outside the panel's model must stay editable: the
// unknown parts are stored as a residual and restored on every push, instead of
// downgrading the server to raw mode and locking the UI.
func TestImportPreservesUnmodelledFieldsAndStaysManaged(t *testing.T) {
	raw := []byte(`{
	  "log": {"level": "info", "timestamp": true, "output": "/var/log/sb.log"},
	  "dns": {"servers": [{"type": "local", "tag": "local"}]},
	  "inbounds": [
	    {"type": "vless", "tag": "main", "listen": "::", "listen_port": 20999,
	     "users": [{"name": "u", "uuid": "11111111-2222-3333-4444-555555555555"}],
	     "smux": {"enabled": true}, "udp_timeout": "5m"},
	    {"type": "wireguard", "tag": "wg-hand", "listen_port": 51820, "private_key": "KEEP"}
	  ],
	  "outbounds": [
	    {"type": "direct", "tag": "direct"},
	    {"type": "tor", "tag": "tor-hand", "executable_path": "/usr/bin/tor"}
	  ],
	  "route": {"final": "direct", "rules": [{"action": "sniff"}], "default_domain_resolver": "local"},
	  "experimental": {"cache_file": {"enabled": true}}
	}`)

	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	db := testDB(t)
	srv := model.Server{Name: "residual", AgentToken: "residual-token"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	app := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
	if err := app.applyImport(&srv, parsed, raw); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&srv, srv.ID).Error; err != nil {
		t.Fatal(err)
	}

	if srv.EffectiveConfigMode() != model.ConfigModeManaged {
		t.Fatalf("config mode = %q, want managed (unknown fields must not lock the panel)", srv.EffectiveConfigMode())
	}
	if len(srv.ConfigResidual) == 0 {
		t.Fatal("residual not persisted")
	}

	generated, err := app.orch.BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(generated, &cfg); err != nil {
		t.Fatal(err)
	}

	// Top-level blocks, log extras and experimental extras survive the push.
	if _, ok := cfg["dns"]; !ok {
		t.Error("dns block lost")
	}
	if log, ok := cfg["log"].(map[string]any); !ok || log["output"] != "/var/log/sb.log" {
		t.Errorf("log.output lost: %v", cfg["log"])
	}
	if exp, ok := cfg["experimental"].(map[string]any); !ok {
		t.Errorf("experimental lost: %v", cfg["experimental"])
	} else if _, ok := exp["cache_file"]; !ok {
		t.Error("experimental.cache_file lost")
	} else if _, ok := exp["clash_api"]; !ok {
		t.Error("panel clash_api missing")
	}
	if route, ok := cfg["route"].(map[string]any); !ok || route["default_domain_resolver"] != "local" {
		t.Errorf("route.default_domain_resolver lost: %v", cfg["route"])
	}

	// The modelled inbound keeps its unknown fields, and the unsupported one is
	// carried over whole.
	inboundTags := map[string]map[string]any{}
	for _, entry := range cfg["inbounds"].([]any) {
		if in, ok := entry.(map[string]any); ok {
			inboundTags[in["tag"].(string)] = in
		}
	}
	main, ok := inboundTags["main"]
	if !ok {
		t.Fatal("modelled inbound missing")
	}
	if _, ok := main["smux"]; !ok {
		t.Error("inbound smux lost")
	}
	if main["udp_timeout"] != "5m" {
		t.Errorf("inbound udp_timeout = %v", main["udp_timeout"])
	}
	if wg, ok := inboundTags["wg-hand"]; !ok {
		t.Error("pass-through wireguard inbound lost")
	} else if wg["private_key"] != "KEEP" {
		t.Errorf("pass-through inbound altered: %v", wg)
	}

	foundTOR := false
	for _, entry := range cfg["outbounds"].([]any) {
		if ob, ok := entry.(map[string]any); ok && ob["tag"] == "tor-hand" {
			foundTOR = ob["executable_path"] == "/usr/bin/tor"
		}
	}
	if !foundTOR {
		t.Error("pass-through tor outbound lost or altered")
	}
}

// Switching a server back to raw mode must drop the residual: raw mode applies
// the administrator's JSON verbatim, so a stale patch would be misleading.
func TestRawModeClearsResidual(t *testing.T) {
	db := testDB(t)
	srv := model.Server{
		Name: "raw-clear", AgentToken: "raw-clear",
		ConfigResidual: model.JSONText(`{"dns":{"servers":[]}}`),
	}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Server{}).Where("id = ?", srv.ID).
		Update("config_residual", model.JSONText(nil)).Error; err != nil {
		t.Fatal(err)
	}
	var reloaded model.Server
	if err := db.First(&reloaded, srv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if residual := decodedResidual(&reloaded); !residual.Empty() {
		t.Errorf("residual survived raw switch: %+v", residual)
	}
}
