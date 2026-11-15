package singbox

import "testing"

func TestClashImportPreservesUnsetSNI(t *testing.T) {
	for _, sni := range []string{"", "cdn.example"} {
		proxy := map[string]any{"type": "trojan", "name": "node", "server": "exit.example", "port": 443, "password": "credential"}
		if sni != "" {
			proxy["sni"] = sni
		}
		node, err := parseClashProxy(proxy)
		if err != nil {
			t.Fatal(err)
		}
		value, present := node.Params["sni"]
		if sni == "" && present || sni != "" && value != sni {
			t.Fatalf("SNI changed: input=%q params=%v", sni, node.Params)
		}
	}
}

func TestClashGeckoOptionsDoNotEraseTopLevelBounds(t *testing.T) {
	node, err := parseClashProxy(map[string]any{
		"type": "hysteria2", "name": "gecko", "server": "exit.example", "port": 443,
		"password": "credential", "obfs": "gecko", "min-packet-size": 800, "max-packet-size": 1200,
		"obfs-opts": map[string]any{"password": "obfs-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if node.Params["gecko_min_packet_size"] != 800 || node.Params["gecko_max_packet_size"] != 1200 {
		t.Fatalf("packet bounds lost: %v", node.Params)
	}
}
