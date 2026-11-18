package panel

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

const realityGuardImportFixture = `{
  "log":{"level":"info"},
  "inbounds":[
    {"type":"direct","tag":"reality-guard","listen":"::","listen_port":31735,"network":"tcp","override_address":"127.0.0.1","override_port":31736},
    {"type":"vless","tag":"reality-in","listen":"127.0.0.1","listen_port":31736,
     "users":[{"uuid":"12345678-1234-1234-1234-123456789012","flow":"xtls-rprx-vision"}],
     "tls":{"enabled":true,"server_name":"music.apple.com","alpn":["h2","http/1.1"],
       "reality":{"enabled":true,"handshake":{"server":"music.apple.com","server_port":443},
         "private_key":"UuMBgl7MXTPx9inmQp2UC7Jcnwc6XYbwDNebonM-FCc","short_id":["abcd"],"max_time_difference":"1m"}}}
  ],
  "outbounds":[{"type":"direct","tag":"direct"}],
  "route":{"rules":[
    {"inbound":["reality-guard"],"action":"sniff","sniffer":["tls"],"timeout":"300ms"},
    {"inbound":["reality-guard"],"domain":["music.apple.com"],"action":"route","outbound":"direct"},
    {"inbound":["reality-guard"],"action":"reject","method":"drop"}
  ],"final":"direct"}
}`

func mutateRealityGuardImport(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal([]byte(realityGuardImportFixture), &root); err != nil {
		t.Fatal(err)
	}
	mutate(root)
	raw, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestImportRealityGuardManagedAndPublicSubscription(t *testing.T) {
	raw := []byte(realityGuardImportFixture)
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if mode := importedConfigMode(t, parsed, raw); mode != model.ConfigModeManaged {
		t.Fatalf("mode=%s, want managed", mode)
	}
	db := testDB(t)
	srv := model.Server{Name: "guard", Address: "node.example.com", AgentToken: "guard-token"}
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
	if srv.EffectiveConfigMode() != model.ConfigModeManaged || !bytes.Equal(srv.RawConfig, raw) {
		t.Fatalf("import lost managed mode or original raw source: %+v", srv)
	}
	var inbounds []model.Inbound
	if err := db.Where("server_id = ?", srv.ID).Find(&inbounds).Error; err != nil {
		t.Fatal(err)
	}
	if len(inbounds) != 1 || inbounds[0].ListenPort != 31735 || inbounds[0].Tag != "reality-in" {
		t.Fatalf("stored inbounds=%+v", inbounds)
	}
	nodes, err := app.gatherNodesWithError(&model.User{ID: 100, ServerIDs: []uint{srv.ID}})
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes=%+v, err=%v", nodes, err)
	}
	client := nodes[0].clientNode()
	if client.ServerPort != 31735 || client.Settings.TLS.Reality.MaxTimeDifference != "1m" {
		t.Fatalf("subscription client=%+v", client)
	}
	link, err := singbox.BuildShareLink(client)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(link)
	if err != nil || uri.Port() != "31735" || uri.Query().Get("sni") != "music.apple.com" || uri.Query().Get("pbk") == "" {
		t.Fatalf("subscription URI=%s, err=%v", link, err)
	}
	clientRaw, err := singbox.BuildClientOutbound(client)
	if err != nil || !strings.Contains(string(clientRaw), `"server_port":31735`) {
		t.Fatalf("client outbound=%s, err=%v", clientRaw, err)
	}
	// Compare the actual orchestrator output too: the imported DB port is the
	// public subscription endpoint, while server generation restores loopback.
	generated, err := app.orch.BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := singbox.ParseServerConfig(generated)
	if err != nil || len(reparsed.Inbounds) != 1 || reparsed.Inbounds[0].ListenPort != 31735 || !reparsed.Inbounds[0].Settings.RealityGuardEnabled() {
		t.Fatalf("orchestrator lost gate: %+v, %v", reparsed, err)
	}
	if mode := importedConfigMode(t, reparsed, generated); mode != model.ConfigModeManaged {
		t.Fatalf("generated mode=%s", mode)
	}
}

func TestImportRealityGuardUnknownDataAndDifferencesStayEditable(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unknown direct field", func(root map[string]any) { root["inbounds"].([]any)[0].(map[string]any)["future"] = true }},
		// An unknown *root* block is preserved as a residual and pushed back
		// verbatim, so it stays managed; see
		// TestImportRealityGuardKeepsUnknownRootBlock below.
		{"unknown reality field", func(root map[string]any) {
			root["inbounds"].([]any)[1].(map[string]any)["tls"].(map[string]any)["reality"].(map[string]any)["future"] = true
		}},
		{"extra guard rule", func(root map[string]any) {
			route := root["route"].(map[string]any)
			route["rules"] = append(route["rules"].([]any), map[string]any{"inbound": []any{"reality-guard"}, "action": "route", "outbound": "direct"})
		}},
		{"shared target", func(root map[string]any) {
			root["inbounds"] = append(root["inbounds"].([]any), map[string]any{"type": "direct", "tag": "another-guard", "listen": "::", "listen_port": float64(31800), "network": "tcp", "override_address": "127.0.0.1", "override_port": float64(31736)})
		}},
		{"named user", func(root map[string]any) {
			root["inbounds"].([]any)[1].(map[string]any)["users"].([]any)[0].(map[string]any)["name"] = "specific-identity"
		}},
		{"disabled timestamp", func(root map[string]any) { root["log"].(map[string]any)["timestamp"] = false }},
		{"different log level", func(root map[string]any) { root["log"].(map[string]any)["level"] = "debug" }},
		{"unknown allow match", func(root map[string]any) {
			root["route"].(map[string]any)["rules"].([]any)[1].(map[string]any)["source_ip_cidr"] = []any{"192.0.2.0/24"}
		}},
		{"weak domain IP OR", func(root map[string]any) {
			allow := root["route"].(map[string]any)["rules"].([]any)[1].(map[string]any)
			allow["ip_cidr"], allow["port"] = []any{"127.0.0.1/32"}, []any{float64(31736)}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := mutateRealityGuardImport(t, test.mutate)
			parsed, err := singbox.ParseServerConfig(raw)
			if err != nil {
				t.Fatal(err)
			}
			if mode := importedConfigMode(t, parsed, raw); mode != model.ConfigModeManaged {
				t.Fatalf("mode=%s, want managed", mode)
			}
			db := testDB(t)
			srv := model.Server{Name: test.name, AgentToken: test.name}
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
			if srv.EffectiveConfigMode() != model.ConfigModeManaged || !bytes.Equal(srv.RawConfig, raw) {
				t.Fatal("imported configuration was locked or source changed")
			}
			generated, err := app.orch.BuildServerConfig(&srv)
			if err != nil || !equivalentJSON(generated, raw) {
				t.Fatalf("raw source changed: %s, %v", generated, err)
			}
		})
	}
}

// A config with a block the panel has no editor for must stay manageable: the
// block is preserved and restored on every push instead of locking the UI.
func TestImportKeepsUnknownRootBlockAndStaysManaged(t *testing.T) {
	raw := mutateRealityGuardImport(t, func(root map[string]any) {
		root["future"] = map[string]any{"enabled": true}
	})
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	db := testDB(t)
	srv := model.Server{Name: "unknown root block", AgentToken: "unknown-root"}
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
		t.Fatalf("mode=%s, want managed", srv.EffectiveConfigMode())
	}
	generated, err := app.orch.BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(generated, &cfg); err != nil {
		t.Fatal(err)
	}
	block, ok := cfg["future"].(map[string]any)
	if !ok || block["enabled"] != true {
		t.Fatalf("unknown root block lost on push: %v", cfg["future"])
	}
}

func TestImportRealityGuardAcceptsOnlyFiniteDefaults(t *testing.T) {
	for _, name := range []any{nil, "", "user"} {
		raw := mutateRealityGuardImport(t, func(root map[string]any) {
			user := root["inbounds"].([]any)[1].(map[string]any)["users"].([]any)[0].(map[string]any)
			if name != nil {
				user["name"] = name
			}
		})
		parsed, err := singbox.ParseServerConfig(raw)
		if err != nil {
			t.Fatal(err)
		}
		if mode := importedConfigMode(t, parsed, raw); mode != model.ConfigModeManaged {
			t.Fatalf("name=%v mode=%s", name, mode)
		}
	}
	// Missing renderer defaults are also preserved on ordinary imports.
	raw := []byte(`{"log":{"level":"info"},"inbounds":[{"type":"socks","tag":"socks","listen":"::","listen_port":1080}],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"}}`)
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if mode := importedConfigMode(t, parsed, raw); mode != model.ConfigModeManaged {
		t.Fatalf("missing timestamp locked editing: %s", mode)
	}
}

func TestImportRealityGuardReservedStatsPortsKeepExistingServices(t *testing.T) {
	for _, port := range []int{29091, 29092} {
		for _, internal := range []bool{true, false} {
			raw := mutateRealityGuardImport(t, func(root map[string]any) {
				inbounds := root["inbounds"].([]any)
				gate, target := inbounds[0].(map[string]any), inbounds[1].(map[string]any)
				if internal {
					gate["override_port"], target["listen_port"] = port, port
				} else {
					gate["listen_port"] = port
				}
			})
			parsed, err := singbox.ParseServerConfig(raw)
			if err != nil {
				t.Fatal(err)
			}
			if mode := importedConfigMode(t, parsed, raw); mode != model.ConfigModeManaged {
				t.Fatalf("reserved port %d internal=%v locked editing: %s", port, internal, mode)
			}
			db := testDB(t)
			srv := model.Server{Name: "reserved guard", AgentToken: "reserved-guard-token", SingboxVersion: "1.14.4"}
			if err := db.Create(&srv).Error; err != nil {
				t.Fatal(err)
			}
			a := &App{db: db, orch: NewOrchestrator(db, NewHub(db))}
			if err := a.applyImport(&srv, parsed, raw); err != nil {
				t.Fatal(err)
			}
			if err := db.First(&srv, srv.ID).Error; err != nil {
				t.Fatal(err)
			}
			generated, err := a.orch.BuildServerConfig(&srv)
			if err != nil || !bytes.Equal(generated, raw) {
				t.Fatalf("raw with reserved port changed: %v", err)
			}
		}
	}
}

func TestImportRealityGuardPreservesInternalInboundRouting(t *testing.T) {
	parsed, err := singbox.ParseServerConfig([]byte(realityGuardImportFixture))
	if err != nil {
		t.Fatal(err)
	}
	inbound := parsed.Inbounds[0]
	generated, err := singbox.BuildServerConfig(singbox.ServerConfigInput{
		Inbounds: []singbox.InboundInput{{Tag: inbound.Tag, Type: inbound.Type, ListenPort: inbound.ListenPort, Settings: inbound.Settings}},
		Rules:    []singbox.RuleInput{{Inbound: []string{"reality-in"}, DomainSuffix: []string{"example.com"}, Outbound: "direct"}},
		Final:    "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := singbox.ParseServerConfig(generated)
	if err != nil {
		t.Fatal(err)
	}
	if len(reparsed.Inbounds) != 1 || !reparsed.Inbounds[0].Settings.RealityGuardEnabled() || len(reparsed.Rules) != 2 {
		t.Fatalf("internal routing blocked gate recognition: %+v", reparsed)
	}
	rule := reparsed.Rules[1]
	if len(rule.Match.Inbound) != 1 || rule.Match.Inbound[0] != "reality-in" || len(rule.Match.DomainSuffix) != 1 || rule.Match.DomainSuffix[0] != "example.com" {
		t.Fatalf("internal inbound routing changed: %+v", rule)
	}
	if mode := importedConfigMode(t, reparsed, generated); mode != model.ConfigModeManaged {
		t.Fatalf("generated gate with internal routing became %s", mode)
	}
}
