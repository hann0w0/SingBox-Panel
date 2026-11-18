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

func TestComplexNestedInboundAndRouteStayManaged(t *testing.T) {
	raw := []byte(`{
	  "log": {"level": "info", "timestamp": true},
	  "inbounds": [
	    {
	      "type": "vless", "tag": "vless-grpc", "listen": "::", "listen_port": 443,
	      "tcp_fast_open": true,
	      "users": [{"name": "user", "uuid": "11111111-2222-3333-4444-555555555555"}],
	      "multiplex": {"enabled": true, "padding": true},
	      "transport": {"type": "grpc", "service_name": "TunService", "idle_timeout": "15s"},
	      "tls": {
	        "enabled": true, "server_name": "edge.example.com",
	        "min_version": "1.2", "max_version": "1.3",
	        "certificate_path": "/etc/ssl/edge.pem", "key_path": "/etc/ssl/edge.key",
	        "ech": {"enabled": true}
	      }
	    },
	    {
	      "type": "mixed", "tag": "local-mixed", "listen": "127.0.0.1", "listen_port": 10808,
	      "set_system_proxy": false
	    },
	    {
	      "type": "tun", "tag": "tun-in", "interface_name": "tun0", "auto_route": true, "mtu": 9000
	    }
	  ],
	  "outbounds": [{"type": "direct", "tag": "direct"}],
	  "route": {
	    "final": "direct",
	    "rules": [
	      {"action": "sniff"},
	      {
	        "domain_regex": ["^ads\\."], "port_range": ["1000:2000"],
	        "process_name": ["curl"], "clash_mode": "rule",
	        "ip_is_private": true, "invert": true,
	        "action": "route", "outbound": "direct"
	      }
	    ]
	  }
	}`)

	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Inbounds) != 3 {
		t.Fatalf("parsed inbounds = %d, want 3 (skipped=%v)", len(parsed.Inbounds), parsed.Skipped)
	}
	db := testDB(t)
	srv := model.Server{Name: "complex", AgentToken: "complex-token"}
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
		t.Fatalf("config mode = %q, want managed for complex nested config", srv.EffectiveConfigMode())
	}

	generated, err := app.orch.BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(generated, &cfg); err != nil {
		t.Fatal(err)
	}
	inbounds := cfg["inbounds"].([]any)
	if len(inbounds) != 3 {
		t.Fatalf("generated inbounds = %d, want 3", len(inbounds))
	}
	vless := inbounds[0].(map[string]any)
	if vless["tcp_fast_open"] != true {
		t.Errorf("tcp_fast_open lost: %v", vless)
	}
	if tr, _ := vless["transport"].(map[string]any); tr["type"] != "grpc" || tr["service_name"] != "TunService" {
		t.Errorf("grpc transport lost: %v", tr)
	}
	if tlsMap, _ := vless["tls"].(map[string]any); tlsMap["min_version"] != "1.2" || tlsMap["ech"] == nil {
		t.Errorf("nested tls fields lost: %v", tlsMap)
	}
	rules := cfg["route"].(map[string]any)["rules"].([]any)
	rule1 := rules[1].(map[string]any)
	if rule1["clash_mode"] != "rule" || rule1["invert"] != true || rule1["ip_is_private"] != true || rule1["domain_regex"] == nil {
		t.Errorf("complex route rule fields lost: %v", rule1)
	}
}
