package panel

import (
	"strings"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/config"
)

func TestSeedRequiresExplicitBootstrapPasswordWhenAdminMissing(t *testing.T) {
	db := testDB(t)
	cfg := config.Default()
	cfg.Admin.Email = "admin"
	cfg.Admin.Password = ""
	if err := seed(db, cfg); err == nil || !strings.Contains(err.Error(), "password is required") {
		t.Fatalf("seed error = %v; want explicit bootstrap-password failure", err)
	}
}
