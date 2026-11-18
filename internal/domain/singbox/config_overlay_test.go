package singbox

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestConfigOverlayUsesCurrentRendererForBothBaselines(t *testing.T) {
	// A renderer may acquire fields after this snapshot was saved. Its new
	// defaults appear on both projections and must not overwrite a custom value.
	p := ConfigProjection{Input: ServerConfigInput{Inbounds: []InboundInput{{Tag: "main", Type: "vless", ListenPort: 443, Settings: InboundSettings{SingleUser: true, UUID: "11111111-2222-3333-4444-555555555555", TLS: TLSSettings{Enabled: true}}}}}, IDs: map[string][]string{"inbounds": {"1"}}}
	raw := []byte(`{"inbounds":[{"type":"vless","tag":"main","listen_port":443,"tls":{"enabled":true,"server_name":"custom.example","future_tls":true},"users":[{"name":"manual","uuid":"11111111-2222-3333-4444-555555555555","extra":true}]}],"outbounds":[{"type":"direct","tag":"direct"}]}`)
	overlay, err := NewConfigOverlay(raw, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(overlay)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the defaults applied by a newer renderer, then edit another field.
	current := p
	current.Input.Inbounds = append([]InboundInput(nil), p.Input.Inbounds...)
	current.Input.Inbounds[0].ListenPort = 8443
	got, err := ApplyConfigOverlay(encoded, current)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(`"server_name": "custom.example"`)) || !bytes.Contains(got, []byte(`"future_tls": true`)) {
		t.Fatalf("new defaults changed raw values: %s", got)
	}
	// A later explicit edit must take ownership of precisely that field.
	current.Input.Inbounds[0].Settings.TLS.ServerName = "edited.example"
	current.Input.Inbounds[0].Settings.UUID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	got, err = ApplyConfigOverlay(encoded, current)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`"server_name": "edited.example"`, `"future_tls": true`, `"extra": true`, `"name": "manual"`, `aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee`} {
		if !bytes.Contains(got, []byte(part)) {
			t.Fatalf("explicit edit lost %s: %s", part, got)
		}
	}
	// A disabled entry must disappear without being resurrected by preservation.
	current.Input.Inbounds = nil
	current.IDs = map[string][]string{}
	got, err = ApplyConfigOverlay(encoded, current)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte(`"tag": "main"`)) {
		t.Fatalf("disabled inbound resurrected: %s", got)
	}
}

func TestMergeConfigJSONNewObjectFieldKeepsUnknownChildren(t *testing.T) {
	got, err := mergeConfigJSON([]byte(`{"tls":{"future":{"custom":true}}}`), []byte(`{"tls":{}}`), []byte(`{"tls":{"future":{"enabled":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"tls":{"future":{"custom":true,"enabled":true}}}` {
		t.Fatalf("newly modelled object lost unknown child: %s", got)
	}
}

func TestConfigOverlayPreservesAbsentAndEmptySections(t *testing.T) {
	p := ConfigProjection{Input: ServerConfigInput{Inbounds: []InboundInput{{Tag: "main", Type: "socks", ListenPort: 1080, Settings: InboundSettings{SingleUser: true}}}}, IDs: map[string][]string{"inbounds": {"1"}}}
	overlay, err := NewConfigOverlay([]byte(`{"inbounds":[{"type":"socks","tag":"main","listen_port":1080}],"outbounds":[]}`), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(overlay)
	p.Input.Inbounds[0].ListenPort = 1081
	got, err := ApplyConfigOverlay(encoded, p)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(got, &root); err != nil {
		t.Fatal(err)
	}
	if _, added := root["route"]; added || string(root["outbounds"]) != "[]" {
		t.Fatalf("absent/empty sections changed: %s", got)
	}
	overlay.Original = json.RawMessage(`null`)
	encoded, _ = json.Marshal(overlay)
	if _, err := ApplyConfigOverlay(encoded, p); err == nil {
		t.Fatal("corrupt snapshot silently accepted")
	}
}
