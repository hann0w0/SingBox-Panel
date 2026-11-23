package singbox

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInboundClientPortValidationAndConfigIsolation(t *testing.T) {
	if got := (InboundSettings{}).EffectiveClientPort(1443); got != 1443 {
		t.Fatalf("unset client port fell back to %d, want 1443", got)
	}
	if got := (InboundSettings{ClientPort: 443}).EffectiveClientPort(1443); got != 443 {
		t.Fatalf("explicit client port = %d, want 443", got)
	}

	valid := InboundSettings{
		Listen:     "127.0.0.1",
		ClientPort: 443,
		SingleUser: true,
		UUID:       "11111111-2222-4333-8444-555555555555",
	}
	if err := valid.Validate("vless"); err != nil {
		t.Fatalf("valid client_port rejected: %v", err)
	}
	for _, bad := range []int{-1, 65536} {
		s := valid
		s.ClientPort = bad
		if err := s.Validate("vless"); err == nil {
			t.Fatalf("out-of-range client_port %d accepted", bad)
		}
	}
	if err := (InboundSettings{ClientPort: 443}).Validate("tun"); err == nil {
		t.Fatal("tun inbound must reject client_port")
	}

	raw, err := BuildServerConfig(ServerConfigInput{
		Inbounds: []InboundInput{{
			Tag:        "vless-behind-nginx",
			Type:       "vless",
			ListenPort: 1443,
			Settings:   valid,
		}},
	})
	if err != nil {
		t.Fatalf("BuildServerConfig: %v", err)
	}
	if strings.Contains(string(raw), "client_port") {
		t.Fatalf("panel-only client_port leaked into sing-box config: %s", raw)
	}
	var doc struct {
		Inbounds []struct {
			Tag        string `json:"tag"`
			Listen     string `json:"listen"`
			ListenPort int    `json:"listen_port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal server config: %v", err)
	}
	if len(doc.Inbounds) != 1 || doc.Inbounds[0].Listen != "127.0.0.1" || doc.Inbounds[0].ListenPort != 1443 {
		t.Fatalf("unexpected generated inbound: %+v", doc.Inbounds)
	}
}
