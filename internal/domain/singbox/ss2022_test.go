package singbox

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestSS2022MultiUserCapabilities(t *testing.T) {
	for _, tc := range []struct {
		method    string
		keyLen    int
		multiUser bool
	}{
		{"2022-blake3-aes-128-gcm", 16, true},
		{"2022-blake3-aes-256-gcm", 32, true},
		{"2022-blake3-chacha20-poly1305", 32, false},
		{"aes-128-gcm", 0, false},
		{"chacha20-ietf-poly1305", 0, false},
	} {
		t.Run(tc.method, func(t *testing.T) {
			settings := InboundSettings{Method: tc.method, MultiUser: true}
			if got := SupportsMultiUser("shadowsocks", settings); got != tc.multiUser {
				t.Errorf("SupportsMultiUser = %v, want %v", got, tc.multiUser)
			}
			if got := settings.UseMultiUser("shadowsocks"); got != tc.multiUser {
				t.Errorf("UseMultiUser = %v, want %v", got, tc.multiUser)
			}
			if got := SS2022KeyLen(tc.method); got != tc.keyLen {
				t.Errorf("key length = %d, want %d", got, tc.keyLen)
			}
			if got := IsSS2022(tc.method); got != (tc.keyLen > 0) {
				t.Errorf("IsSS2022 = %v", got)
			}
		})
	}
}

func TestSS2022ChaChaRejectsExplicitMultiUser(t *testing.T) {
	for _, singleUser := range []bool{false, true} {
		settings := InboundSettings{
			Method: "2022-blake3-chacha20-poly1305", SSServerPSK: "server-key",
			MultiUser: true, SingleUser: singleUser,
		}
		if err := settings.Validate("shadowsocks"); err == nil || !strings.Contains(err.Error(), "multi-user") {
			t.Errorf("single_user=%v validation = %v, want unsupported multi-user error", singleUser, err)
		}
		if _, err := BuildInbound(InboundInput{Type: "shadowsocks", Settings: settings, Users: sampleUsers()}); err == nil {
			t.Errorf("single_user=%v: invalid multi-user inbound was rendered", singleUser)
		}
		node := ClientNode{Type: "shadowsocks", Settings: settings, User: sampleUsers()[0]}
		if _, err := BuildClientOutbound(node); err == nil {
			t.Errorf("single_user=%v: invalid multi-user outbound was rendered", singleUser)
		}
		if _, err := BuildShareLink(node); err == nil {
			t.Errorf("single_user=%v: invalid multi-user share link was rendered", singleUser)
		}
	}
	// Older domain callers infer the wire mode from Users without MultiUser.
	if _, err := BuildInbound(InboundInput{
		Type: "shadowsocks", Users: sampleUsers(),
		Settings: InboundSettings{Method: "2022-blake3-chacha20-poly1305", SSServerPSK: "server-key"},
	}); err == nil {
		t.Fatal("ChaCha user list must not be rendered or silently discarded")
	}
}

func TestSS2022ChaChaSingleUserKeepsSharedKey(t *testing.T) {
	psk := base64.StdEncoding.EncodeToString(make([]byte, 32))
	for _, singleUser := range []bool{false, true} {
		settings := InboundSettings{Method: "2022-blake3-chacha20-poly1305", SSServerPSK: psk, SingleUser: singleUser}
		raw, err := BuildInbound(InboundInput{Tag: "ss", Type: "shadowsocks", ListenPort: 8388, Settings: settings})
		if err != nil {
			t.Fatal(err)
		}
		var inbound map[string]any
		if err := json.Unmarshal(raw, &inbound); err != nil {
			t.Fatal(err)
		}
		if _, ok := inbound["users"]; ok || inbound["password"] != psk {
			t.Fatalf("single_user=%v: invalid ChaCha wire shape: %s", singleUser, raw)
		}
		if got := SSClientPassword(settings, "user-secret"); got != psk {
			t.Fatalf("single-user ChaCha password = %q, want shared key", got)
		}
		node := ClientNode{Name: "ss", Type: "shadowsocks", Server: "node.example.com", ServerPort: 8388, Settings: settings, User: sampleUsers()[0]}
		raw, err = BuildClientOutbound(node)
		if err != nil {
			t.Fatal(err)
		}
		var outbound map[string]any
		if err := json.Unmarshal(raw, &outbound); err != nil || outbound["password"] != psk {
			t.Fatalf("client key changed: %s, err=%v", raw, err)
		}
		link, err := BuildShareLink(node)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseShareLink(link)
		if err != nil || parsed.Settings.SSServerPSK != psk {
			t.Fatalf("share-link key changed: %+v, err=%v", parsed, err)
		}
	}
}

func TestSS2022AESMultiUserKeysMatchSubscriptions(t *testing.T) {
	for _, method := range []string{"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm"} {
		t.Run(method, func(t *testing.T) {
			psk := base64.StdEncoding.EncodeToString(make([]byte, SS2022KeyLen(method)))
			settings := InboundSettings{Method: method, MultiUser: true, SSServerPSK: psk}
			users := sampleUsers()
			raw, err := BuildInbound(InboundInput{Tag: "ss", Type: "shadowsocks", ListenPort: 8388, Settings: settings, Users: users})
			if err != nil {
				t.Fatal(err)
			}
			var inbound struct {
				Users []struct {
					Password string `json:"password"`
				} `json:"users"`
			}
			if err := json.Unmarshal(raw, &inbound); err != nil || len(inbound.Users) != len(users) {
				t.Fatalf("invalid user list: %s, err=%v", raw, err)
			}
			for i, user := range users {
				key := inbound.Users[i].Password
				decoded, err := base64.StdEncoding.DecodeString(key)
				if err != nil || len(decoded) != SS2022KeyLen(method) {
					t.Fatalf("invalid derived user key: %q", key)
				}
				if got := SSClientPassword(settings, user.Password); got != psk+":"+key {
					t.Fatalf("client password = %q, does not match inbound", got)
				}
			}
			settings.SingleUser = true
			settings.MultiUser = false
			if got := SSClientPassword(settings, "unused-user-key"); got != psk {
				t.Fatalf("single-user AES key changed: %q", got)
			}
		})
	}
}
