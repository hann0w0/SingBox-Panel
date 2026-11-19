package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestRouteOverlayAPIKeepsRulesThroughEditReorderAndReimport(t *testing.T) {
	db := testDB(t)
	srv := model.Server{Name: "route-overlay", AgentToken: "route-overlay"}
	if err := db.Create(&srv).Error; err != nil {
		t.Fatal(err)
	}
	app := &App{db: db, hub: NewHub(db), orch: NewOrchestrator(db, NewHub(db))}
	router := gin.New()
	router.POST("/servers/:id/rules", app.createRule)
	router.PUT("/servers/:id/rules/reorder", app.reorderRules)
	router.PUT("/servers/:id/rules/:ruleID", app.updateRule)
	router.DELETE("/servers/:id/rules/:ruleID", app.deleteRule)
	router.GET("/servers/:id/rules", app.listRules)
	raw, err := json.Marshal(map[string]any{
		"outbounds":    []any{map[string]any{"type": "direct", "tag": "direct"}},
		"experimental": map[string]any{"clash_api": map[string]any{"external_controller": "127.0.0.1:29091", "secret": "secret"}},
		"route": map[string]any{"rules": []any{
			map[string]any{"action": "future-a", "large": json.Number("9007199254740993")},
			singbox.LoopbackGuardRule(),
			map[string]any{"action": "sniff"},
			map[string]any{"port": []int{80}, "outbound": "direct", "marker": "A"},
			map[string]any{"action": "resolve", "strategy": "prefer_ipv4", "marker": "B"},
			map[string]any{"action": "future-b", "nested": map[string]any{"keep": true}},
			map[string]any{"port": []int{443}, "outbound": "direct", "marker": "C"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("/servers/%d/rules", srv.ID)
	request := func(method, path, body string) {
		t.Helper()
		res := serveJSONRequest(t, router, method, path, body)
		if res.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, res.Code, res.Body)
		}
	}
	var byMarker map[string]model.RouteRule
	for round := 0; round < 3; round++ {
		parsed, err := singbox.ParseServerConfig(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := app.applyImport(&srv, parsed, raw); err != nil {
			t.Fatal(err)
		}
		var rows []model.RouteRule
		if err := db.Where("server_id = ?", srv.ID).Order("sort, id").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		byMarker = make(map[string]model.RouteRule)
		for _, row := range rows {
			var match singbox.RuleInput
			if err := json.Unmarshal(row.Match, &match); err != nil {
				t.Fatal(err)
			}
			marker, _ := match.Extra["marker"].(string)
			if marker == "" {
				marker = match.Action
			}
			byMarker[marker] = row
		}
		if len(rows) != 4 || len(byMarker) != 4 {
			t.Fatalf("import duplicated or hid form rules: %+v", rows)
		}
		order := []uint{byMarker["C"].ID, byMarker["B"].ID, byMarker["A"].ID, byMarker["sniff"].ID}
		body, _ := json.Marshal(map[string]any{"order": order})
		request(http.MethodPut, base+"/reorder", string(body))
		for _, marker := range []string{"A", "B"} {
			row := byMarker[marker]
			var match map[string]any
			_ = json.Unmarshal(row.Match, &match)
			if marker == "A" {
				match["port"] = []int{81 + round}
			} else {
				match["strategy"] = "prefer_ipv6"
			}
			body, _ := json.Marshal(map[string]any{"match": match, "outbound": row.Outbound})
			request(http.MethodPut, fmt.Sprintf("%s/%d", base, row.ID), string(body))
		}
		response := serveJSONRequest(t, router, http.MethodGet, base, "")
		var listed struct {
			Rules []model.RouteRule `json:"rules"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &listed) != nil || len(listed.Rules) != len(order) {
			t.Fatalf("invalid rule list: %s", response.Body)
		}
		for i, row := range listed.Rules {
			if row.ID != order[i] {
				t.Fatalf("form order differs from saved order: %+v", listed.Rules)
			}
		}
		if err := db.First(&srv, srv.ID).Error; err != nil {
			t.Fatal(err)
		}
		raw, err = app.orch.BuildServerConfig(&srv)
		if err != nil {
			t.Fatal(err)
		}
		var root struct {
			Route struct {
				Rules []map[string]json.RawMessage `json:"rules"`
			} `json:"route"`
		}
		if err := json.Unmarshal(raw, &root); err != nil {
			t.Fatal(err)
		}
		rules := root.Route.Rules
		if len(rules) != 7 || string(rules[0]["marker"]) != `"C"` || string(rules[1]["marker"]) != `"B"` || string(rules[2]["marker"]) != `"A"` || string(rules[3]["action"]) != `"sniff"` || string(rules[4]["action"]) != `"future-a"` || string(rules[5]["action"]) != `"future-b"` {
			t.Fatalf("round %d mixed, reordered or duplicated rules: %s", round, raw)
		}
		if !equivalentJSON(rules[0]["port"], []byte(`[443]`)) || rules[1]["port"] != nil || string(rules[1]["strategy"]) != `"prefer_ipv6"` || !equivalentJSON(rules[2]["port"], []byte(fmt.Sprintf("[%d]", 81+round))) || string(rules[4]["large"]) != "9007199254740993" || !equivalentJSON(rules[5]["nested"], []byte(`{"keep":true}`)) {
			t.Fatalf("round %d moved rule contents: %s", round, raw)
		}
		var guard map[string]any
		encoded, _ := json.Marshal(rules[6])
		_ = json.Unmarshal(encoded, &guard)
		if !singbox.IsLoopbackGuardRule(guard) {
			t.Fatalf("default guard is not last: %s", raw)
		}
	}
	request(http.MethodPut, fmt.Sprintf("%s/%d", base, byMarker["B"].ID), `{"enabled":false}`)
	request(http.MethodDelete, fmt.Sprintf("%s/%d", base, byMarker["C"].ID), "")
	request(http.MethodPost, base, `{"match":{"port":[8443],"marker":"D"},"outbound":"direct"}`)
	got, err := app.orch.BuildServerConfig(&srv)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Route struct {
			Rules []map[string]json.RawMessage `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(got, &root); err != nil {
		t.Fatal(err)
	}
	rules := root.Route.Rules
	if len(rules) != 6 || string(rules[0]["marker"]) != `"D"` || string(rules[1]["marker"]) != `"A"` || !equivalentJSON(rules[1]["port"], []byte(`[83]`)) || string(rules[2]["action"]) != `"sniff"` || string(rules[3]["action"]) != `"future-a"` || string(rules[4]["action"]) != `"future-b"` || string(rules[5]["action"]) != `"reject"` {
		t.Fatalf("create/disable/delete restored or mixed rules: %s", got)
	}
}
