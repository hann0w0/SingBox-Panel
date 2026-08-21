package config

import "testing"

func TestPanelConfigValidateRequiresHTTPSOutsideDevelopment(t *testing.T) {
	for _, cfg := range []PanelConfig{
		{Environment: "production", BaseURL: "http://panel.example.com"},
		{Environment: "", BaseURL: "http://panel.example.com"},
	} {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Validate() accepted insecure production config: %#v", cfg)
		}
	}
}

func TestPanelConfigValidateAllowsLocalHTTPOnlyInDevelopment(t *testing.T) {
	if err := (PanelConfig{Environment: "development", BaseURL: "http://127.0.0.1:8080"}).Validate(); err != nil {
		t.Fatalf("development HTTP config rejected: %v", err)
	}
	if err := (PanelConfig{Environment: "production", BaseURL: "https://panel.example.com"}).Validate(); err != nil {
		t.Fatalf("production HTTPS config rejected: %v", err)
	}
}

func TestPanelConfigValidateRejectsAmbiguousURLs(t *testing.T) {
	for _, raw := range []string{
		"panel.example.com",
		"https://user:pass@panel.example.com",
		"https://panel.example.com/?token=secret",
		"https://panel.example.com/#admin",
	} {
		if err := (PanelConfig{Environment: "production", BaseURL: raw}).Validate(); err == nil {
			t.Fatalf("Validate() accepted unsafe URL %q", raw)
		}
	}
}
