package singbox

import "testing"

func TestSOCKSClientCredentialsDoNotUseInboundRestrictions(t *testing.T) {
	for _, tc := range []struct {
		name, username, password string
	}{
		{"anonymous", "", ""},
		{"authenticated", "alice", "secret"},
		{"username only", "alice", ""},
		{"password only", "", "secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := InboundSettings{Username: tc.username, Password: tc.password}
			if err := settings.ValidateClientOutbound("socks"); err != nil {
				t.Fatalf("external SOCKS credentials rejected: %v", err)
			}
			partial := (tc.username == "") != (tc.password == "")
			if err := settings.Validate("socks"); (err != nil) != partial {
				t.Fatalf("inbound validation = %v, want error: %v", err, partial)
			}
		})
	}
}
