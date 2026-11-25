package panel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestPrivateRouteScope(t *testing.T) {
	fixture := newForwardFixture(t)
	forward := fixture.create(t)
	foreign := model.User{Email: "bob", Enabled: true, ProxyToken: "bob-seed"}
	mustCreate(t, fixture.db, &foreign)
	staleOwner := fixture.user
	staleOwner.ProxyToken = "previous-seed"
	otherOutbound := fixture.outbound
	otherOutbound.ID = 0
	otherOutbound.Tag = "other-private"
	mustCreate(t, fixture.db, &otherOutbound)
	foreignForward := model.UserForward{UserID: foreign.ID, ServerID: fixture.server.ID, InboundID: fixture.inbound.ID, OutboundID: fixture.outbound.ID}
	mustCreate(t, fixture.db, &foreignForward)
	for _, test := range []struct {
		name     string
		serverID uint
		outbound model.Outbound
		match    singbox.RuleInput
		allowed  bool
	}{
		{name: "owner account without inbound", match: singbox.RuleInput{AuthUser: []string{proxyTrafficName(&fixture.user)}}, allowed: true},
		{name: "owner account with invert false", match: singbox.RuleInput{AuthUser: []string{proxyTrafficName(&fixture.user)}, Extra: map[string]any{"invert": false}}, allowed: true},
		{name: "owner account inverted", match: singbox.RuleInput{AuthUser: []string{proxyTrafficName(&fixture.user)}, Extra: map[string]any{"invert": true}}},
		{name: "corresponding forward", match: singbox.RuleInput{AuthUser: []string{forwardTrafficName(&fixture.user, forward.ID)}, Inbound: []string{fixture.inbound.Tag}}, allowed: true},
		{name: "corresponding forward inverted", match: singbox.RuleInput{AuthUser: []string{forwardTrafficName(&fixture.user, forward.ID)}, Inbound: []string{fixture.inbound.Tag}, Extra: map[string]any{"invert": true}}},
		{name: "global", match: singbox.RuleInput{}},
		{name: "foreign account", match: singbox.RuleInput{AuthUser: []string{proxyTrafficName(&foreign)}}},
		{name: "multiple accounts", match: singbox.RuleInput{AuthUser: []string{proxyTrafficName(&fixture.user), proxyTrafficName(&foreign)}}},
		{name: "stale owner", match: singbox.RuleInput{AuthUser: []string{proxyTrafficName(&staleOwner)}}},
		{name: "stale forward", match: singbox.RuleInput{AuthUser: []string{forwardTrafficName(&staleOwner, forward.ID)}, Inbound: []string{fixture.inbound.Tag}}},
		{name: "missing forward inbound", match: singbox.RuleInput{AuthUser: []string{forwardTrafficName(&fixture.user, forward.ID)}}},
		{name: "wrong forward inbound", match: singbox.RuleInput{AuthUser: []string{forwardTrafficName(&fixture.user, forward.ID)}, Inbound: []string{"other-in"}}},
		{name: "multiple forward inbounds", match: singbox.RuleInput{AuthUser: []string{forwardTrafficName(&fixture.user, forward.ID)}, Inbound: []string{fixture.inbound.Tag, "other-in"}}},
		{name: "wrong server", serverID: fixture.server.ID + 1, match: singbox.RuleInput{AuthUser: []string{forwardTrafficName(&fixture.user, forward.ID)}, Inbound: []string{fixture.inbound.Tag}}},
		{name: "wrong outbound", outbound: otherOutbound, match: singbox.RuleInput{AuthUser: []string{forwardTrafficName(&fixture.user, forward.ID)}, Inbound: []string{fixture.inbound.Tag}}},
		{name: "forward owned by foreign account", match: singbox.RuleInput{AuthUser: []string{forwardTrafficName(&foreign, foreignForward.ID)}, Inbound: []string{fixture.inbound.Tag}}},
		{name: "nonexistent forward", match: singbox.RuleInput{AuthUser: []string{forwardTrafficName(&fixture.user, forward.ID+100)}, Inbound: []string{fixture.inbound.Tag}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			serverID := test.serverID
			if serverID == 0 {
				serverID = fixture.server.ID
			}
			outbound := test.outbound
			if outbound.ID == 0 {
				outbound = fixture.outbound
			}
			err := validatePrivateRouteScope(fixture.db, serverID, outbound, test.match)
			if (err == nil) != test.allowed {
				t.Fatalf("allowed=%v error=%v", test.allowed, err)
			}
		})
	}
}

func TestImportedPrivateRoutingRawScopes(t *testing.T) {
	fixture := newForwardFixture(t)
	forward := fixture.create(t)
	owner := proxyTrafficName(&fixture.user)
	identity := forwardTrafficName(&fixture.user, forward.ID)
	for _, test := range []struct {
		name    string
		route   map[string]any
		extra   map[string]any
		allowed bool
	}{
		{name: "owner array", route: map[string]any{"rules": []any{map[string]any{"outbound": fixture.outbound.Tag, "auth_user": []string{owner}}}}, allowed: true},
		{name: "owner scalar", route: map[string]any{"rules": []any{map[string]any{"outbound": fixture.outbound.Tag, "auth_user": owner}}}, allowed: true},
		{name: "explicit default owner", route: map[string]any{"rules": []any{map[string]any{"type": "default", "invert": false, "outbound": fixture.outbound.Tag, "auth_user": owner}}}, allowed: true},
		{name: "forward scalar", route: map[string]any{"rules": []any{map[string]any{"outbound": fixture.outbound.Tag, "auth_user": identity, "inbound": fixture.inbound.Tag}}}, allowed: true},
		{name: "global hidden from parsed config", route: map[string]any{"rules": []any{map[string]any{"outbound": fixture.outbound.Tag}}}},
		{name: "foreign identity", route: map[string]any{"rules": []any{map[string]any{"outbound": fixture.outbound.Tag, "auth_user": "foreign"}}}},
		{name: "private final hidden by parser fallback", route: map[string]any{"final": fixture.outbound.Tag}},
		{name: "private default", route: map[string]any{"default_outbound": fixture.outbound.Tag}},
		{name: "selector private default", extra: map[string]any{"outbounds": []any{map[string]any{"type": "selector", "tag": "group", "outbounds": []string{"direct"}, "default": fixture.outbound.Tag}}}},
		{name: "selector private member", route: map[string]any{"rules": []any{map[string]any{"outbound": "group"}}}, extra: map[string]any{"outbounds": []any{map[string]any{"type": "selector", "tag": "group", "outbounds": []string{fixture.outbound.Tag}}}}},
		{name: "urltest private member", route: map[string]any{"final": "group"}, extra: map[string]any{"outbounds": []any{map[string]any{"type": "urltest", "tag": "group", "outbounds": []string{"direct", fixture.outbound.Tag}}}}},
		{name: "nested group private member", route: map[string]any{"final": "outer"}, extra: map[string]any{"outbounds": []any{map[string]any{"type": "selector", "tag": "outer", "outbounds": []string{"inner"}}, map[string]any{"type": "urltest", "tag": "inner", "outbounds": []string{fixture.outbound.Tag}}}}},
		{name: "unreferenced group private member", extra: map[string]any{"outbounds": []any{map[string]any{"type": "selector", "tag": "group", "outbounds": []string{fixture.outbound.Tag}}}}},
		{name: "public group members", route: map[string]any{"final": "group"}, extra: map[string]any{"outbounds": []any{map[string]any{"type": "selector", "tag": "group", "outbounds": []string{"direct"}, "default": "direct"}}}, allowed: true},
		{name: "legacy rule set detour", route: map[string]any{"rule_set": []any{map[string]any{"type": "remote", "tag": "rules", "download_detour": fixture.outbound.Tag}}}},
		{name: "http client detour", extra: map[string]any{"http_clients": []any{map[string]any{"tag": "rules-client", "detour": fixture.outbound.Tag}}}},
		{name: "nested unscoped logical", route: map[string]any{"rules": []any{map[string]any{"type": "logical", "mode": "or", "rules": []any{map[string]any{"outbound": fixture.outbound.Tag}}}}}},
		{name: "nested foreign logical", route: map[string]any{"rules": []any{map[string]any{"type": "logical", "mode": "and", "rules": []any{map[string]any{"outbound": fixture.outbound.Tag, "auth_user": "foreign"}}}}}},
		{name: "inverted owner", route: map[string]any{"rules": []any{map[string]any{"outbound": fixture.outbound.Tag, "auth_user": owner, "invert": true}}}},
		{name: "invalid invert owner", route: map[string]any{"rules": []any{map[string]any{"outbound": fixture.outbound.Tag, "auth_user": owner, "invert": "false"}}}},
		{name: "inverted ancestor", route: map[string]any{"rules": []any{map[string]any{"type": "logical", "invert": true, "rules": []any{map[string]any{"outbound": fixture.outbound.Tag, "auth_user": owner}}}}}},
		{name: "nested explicit owner", route: map[string]any{"rules": []any{map[string]any{"type": "logical", "mode": "and", "rules": []any{map[string]any{"outbound": fixture.outbound.Tag, "auth_user": owner}}}}}, allowed: true},
		{name: "logical and target ignores outer identity", route: map[string]any{"rules": []any{map[string]any{"type": "logical", "mode": "and", "outbound": fixture.outbound.Tag, "auth_user": owner, "rules": []any{map[string]any{"domain_suffix": []string{"example.com"}}}}}}},
		{name: "logical or target ignores outer identity", route: map[string]any{"rules": []any{map[string]any{"type": "logical", "mode": "or", "outbound": fixture.outbound.Tag, "auth_user": owner, "rules": []any{map[string]any{"domain_suffix": []string{"example.com"}}}}}}},
		{name: "unsupported rule type", route: map[string]any{"rules": []any{map[string]any{"type": "future", "outbound": fixture.outbound.Tag, "auth_user": owner}}}},
		{name: "invalid rule type", route: map[string]any{"rules": []any{map[string]any{"type": nil, "outbound": fixture.outbound.Tag, "auth_user": owner}}}},
		{name: "no inherited scope across or", route: map[string]any{"rules": []any{map[string]any{"type": "logical", "mode": "or", "auth_user": owner, "rules": []any{map[string]any{"outbound": fixture.outbound.Tag}, map[string]any{"auth_user": owner}}}}}},
		{name: "no inherited scope through nested or", route: map[string]any{"rules": []any{map[string]any{"type": "default", "outbound": fixture.outbound.Tag, "auth_user": owner, "rules": []any{map[string]any{"type": "logical", "mode": "or", "rules": []any{map[string]any{"outbound": fixture.outbound.Tag}}}}}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := map[string]any{"route": test.route}
			for key, value := range test.extra {
				root[key] = value
			}
			raw, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := singbox.ParseServerConfig(raw)
			if err != nil {
				t.Fatal(err)
			}
			err = validateImportedPrivateRouting(fixture.db, fixture.server.ID, parsed, raw)
			if (err == nil) != test.allowed {
				t.Fatalf("allowed=%v error=%v raw=%s", test.allowed, err, raw)
			}
		})
	}
}

func TestImportedPrivateRoutingParsedScopes(t *testing.T) {
	fixture := newForwardFixture(t)
	for _, test := range []struct {
		name   string
		parsed singbox.ParsedConfig
	}{
		{name: "unscoped rule", parsed: singbox.ParsedConfig{Rules: []singbox.ParsedRule{{Outbound: fixture.outbound.Tag}}}},
		{name: "private final", parsed: singbox.ParsedConfig{Final: fixture.outbound.Tag}},
		{name: "private download detour", parsed: singbox.ParsedConfig{RuleSets: []singbox.RuleSetInput{{Tag: "rules", DownloadDetour: fixture.outbound.Tag}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateImportedPrivateRouting(fixture.db, fixture.server.ID, &test.parsed, []byte(`{}`)); err == nil {
				t.Fatal("private routing without owner scope was accepted")
			}
		})
	}
}

func TestPrivateImportScopeFailurePreservesExistingData(t *testing.T) {
	fixture := newForwardFixture(t)
	forward := fixture.create(t)
	mustCreate(t, fixture.db, &model.UserNodeOrder{UserID: fixture.user.ID, NodeType: userNodeTypeForward, NodeID: forward.ID})
	var previous model.Server
	if err := fixture.db.First(&previous, fixture.server.ID).Error; err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"outbounds":[{"type":"vless","tag":"alice-home","server":"home.example","server_port":443,"uuid":"11111111-2222-4333-8444-555555555555"}],"route":{"rules":[{"outbound":"alice-home"}]}}`)
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateImportedPrivateOutbounds(fixture.db, parsed.Outbounds, raw); err != nil {
		t.Fatalf("credential validation unexpectedly failed: %v", err)
	}
	if err := fixture.app.applyImport(&fixture.server, parsed, raw); err == nil || !strings.Contains(err.Error(), "专属出站") {
		t.Fatalf("unsafe import error=%v", err)
	}
	for _, row := range []any{&model.Inbound{}, &model.Outbound{}, &model.UserForward{}, &model.RouteRule{}, &model.UserNodeOrder{}} {
		var count int64
		if err := fixture.db.Model(row).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("failed import changed %T: count=%d error=%v", row, count, err)
		}
	}
	var server model.Server
	if err := fixture.db.First(&server, fixture.server.ID).Error; err != nil {
		t.Fatal(err)
	}
	if server.ConfigMode != previous.ConfigMode || string(server.RawConfig) != string(previous.RawConfig) {
		t.Fatal("failed import changed server source of truth")
	}
}

func TestPrivateImportOwnerRouteWithoutInbound(t *testing.T) {
	fixture := newForwardFixture(t)
	raw, err := json.Marshal(map[string]any{
		"outbounds": []any{map[string]any{"type": "vless", "tag": fixture.outbound.Tag, "server": "home.example", "server_port": 443, "uuid": "11111111-2222-4333-8444-555555555555"}},
		"route":     map[string]any{"rules": []any{map[string]any{"outbound": fixture.outbound.Tag, "auth_user": []string{proxyTrafficName(&fixture.user)}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := singbox.ParseServerConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.applyImport(&fixture.server, parsed, raw); err != nil {
		t.Fatalf("owner import rejected: %v", err)
	}
	var rule model.RouteRule
	if err := fixture.db.Where("server_id = ? AND outbound = ?", fixture.server.ID, fixture.outbound.Tag).First(&rule).Error; err != nil {
		t.Fatal(err)
	}
	var match singbox.RuleInput
	if err := json.Unmarshal(rule.Match, &match); err != nil {
		t.Fatal(err)
	}
	if len(match.AuthUser) != 1 || match.AuthUser[0] != proxyTrafficName(&fixture.user) || len(match.Inbound) != 0 {
		t.Fatalf("owner scope changed: %+v", match)
	}
	var shared int64
	if err := fixture.db.Model(&model.Outbound{}).Where("owner_user_id = 0").Count(&shared).Error; err != nil || shared != 0 {
		t.Fatalf("private outbound copied: count=%d error=%v", shared, err)
	}
}

func TestValidateRuleActionAndSecurityConstraints(t *testing.T) {
	fixture := newForwardFixture(t)
	owner := proxyTrafficName(&fixture.user)

	cases := []struct {
		name     string
		outbound string
		raw      string
		wantErr  string
	}{
		{
			name:     "private outbound rejects invert true",
			outbound: fixture.outbound.Tag,
			raw:      `{"action":"route","auth_user":["` + owner + `"],"invert":true}`,
			wantErr:  "身份范围不能取反",
		},
		{
			name:     "private outbound allows invert false",
			outbound: fixture.outbound.Tag,
			raw:      `{"action":"route","auth_user":["` + owner + `"],"invert":false}`,
		},
		{
			name:     "empty object extra does not count as match",
			outbound: "direct",
			raw:      `{"action":"route","interface_address":{}}`,
			wantErr:  "至少需要一个匹配条件",
		},
		{
			name:     "reject rejects route-options field",
			outbound: "block",
			raw:      `{"action":"reject","domain":["a.com"],"tls_fragment":true}`,
			wantErr:  "仅支持用于",
		},
		{
			name:     "resolve rejects negative rewrite_ttl",
			outbound: "resolve",
			raw:      `{"action":"resolve","rewrite_ttl":-1}`,
			wantErr:  "不能小于 0",
		},
		{
			name:     "resolve rejects invalid timeout",
			outbound: "resolve",
			raw:      `{"action":"resolve","timeout":"0ms"}`,
			wantErr:  "时长必须大于 0",
		},
		{
			name:     "resolve rejects invalid client_subnet",
			outbound: "resolve",
			raw:      `{"action":"resolve","client_subnet":"not-a-cidr"}`,
			wantErr:  "合法的 IP 或 CIDR",
		},
		{
			name:     "resolve accepts valid options",
			outbound: "resolve",
			raw:      `{"action":"resolve","timeout":"5s","rewrite_ttl":0,"client_subnet":"1.1.1.0/24"}`,
		},
		{
			name:     "route accepts legacy string network",
			outbound: "direct",
			raw:      `{"action":"route","network":"tcp"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := fixture.app.validateRule(fixture.db, fixture.server.ID, json.RawMessage(tc.raw), tc.outbound)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid rule, got error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}
