package agent

import "testing"

func TestValidatePanelURLRequiresTLSOutsideDevelopment(t *testing.T) {
	for _, raw := range []string{"http://panel.example.com", "ws://panel.example.com"} {
		if err := ValidatePanelURL(raw, false, "production"); err == nil {
			t.Fatalf("accepted insecure production URL %q", raw)
		}
	}
	if err := ValidatePanelURL("https://panel.example.com", true, "production"); err == nil {
		t.Fatal("accepted --insecure in production")
	}
}

func TestValidatePanelURLAllowsExplicitDevelopmentExceptions(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:32334", "ws://127.0.0.1:32334"} {
		if err := ValidatePanelURL(raw, true, "development"); err != nil {
			t.Fatalf("development URL %q rejected: %v", raw, err)
		}
	}
}
