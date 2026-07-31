package agent

import "testing"

func TestParseLocalTrafficConfig(t *testing.T) {
	cfg, err := parseLocalTrafficConfig([]byte(`{
		"experimental":{"clash_api":{"external_controller":"127.0.0.1:29091","secret":"local"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "http://127.0.0.1:29091/connections" || cfg.Secret != "local" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestParseLocalTrafficConfigRejectsRemoteEndpoint(t *testing.T) {
	_, err := parseLocalTrafficConfig([]byte(`{
		"experimental":{"clash_api":{"external_controller":"198.51.100.1:9090"}}
	}`))
	if err == nil {
		t.Fatal("expected non-loopback endpoint to be rejected")
	}
}
