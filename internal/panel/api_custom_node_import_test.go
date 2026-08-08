package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/singbox"
)

func TestPreviewCustomNodeImportDoesNotWrite(t *testing.T) {
	db := testDB(t)
	app := &App{db: db}
	router := gin.New()
	router.POST("/preview", app.previewCustomNodeImport)
	body := map[string]any{"source": `proxies:
  - name: Preview Trojan
    type: trojan
    server: tr.example.com
    port: 443
    password: secret
    skip-cert-verify: true
    udp: true
`}
	payload, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/preview", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("preview status = %d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Nodes      []singbox.ImportedNode `json:"nodes"`
		Items      []singbox.ImportedNode `json:"items"`
		Skipped    []singbox.ImportIssue  `json:"skipped"`
		SourceType string                 `json:"source_type"`
		Count      int                    `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Count != 1 || len(response.Nodes) != 1 || len(response.Items) != 1 || response.SourceType != "clash-yaml" {
		t.Fatalf("preview response = %+v", response)
	}
	if response.Nodes[0].Params["insecure"] != true || response.Nodes[0].Params["udp"] != true {
		t.Fatalf("preview params = %#v", response.Nodes[0].Params)
	}
	var count int64
	if err := db.Model(&model.CustomNode{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("preview wrote %d nodes (err=%v)", count, err)
	}
}

func TestImportCustomNodesIsTransactionalAndUnassigned(t *testing.T) {
	db := testDB(t)
	app := &App{db: db}
	router := gin.New()
	router.POST("/import", app.importCustomNodes)
	body := map[string]any{
		"source": strings.Join([]string{
			"trojan://secret@tr.example.com:443?sni=tr.example.com&skip-cert-verify=true#One",
			"vless://a2b0c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d@vl.example.com:8443?security=tls&sni=vl.example.com#Two",
			"bad-item",
		}, "\n"),
		// These fields deliberately try to assign the imported nodes. They are
		// ignored by the endpoint; assignment belongs to /users/:id/access.
		"all_users": true,
		"user_ids":  []uint{7},
	}
	payload, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/import", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("import status = %d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Nodes   []model.CustomNode    `json:"nodes"`
		Skipped []singbox.ImportIssue `json:"skipped"`
		Count   int                   `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Count != 2 || len(response.Nodes) != 2 || len(response.Skipped) != 1 {
		t.Fatalf("import response = %+v", response)
	}
	var rows []model.CustomNode
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("stored nodes = %d", len(rows))
	}
	for _, row := range rows {
		if row.AllUsers || len(row.UserIDs) != 0 || len(row.ExcludedUserIDs) != 0 {
			t.Fatalf("imported node was assigned: %+v", row)
		}
		if !row.Enabled || row.ID == 0 || row.Protocol == "" || row.Address == "" || row.Port == 0 {
			t.Fatalf("stored node incomplete: %+v", row)
		}
	}
	if !strings.Contains(rows[0].Link, "skip-cert-verify=true") {
		// The original spelling is intentionally preserved instead of a lossy
		// rebuild, including the certificate verification flag.
		t.Fatalf("original Trojan link was not preserved: %q", rows[0].Link)
	}
}

func TestImportCustomNodesStoresStructuredClashParams(t *testing.T) {
	db := testDB(t)
	app := &App{db: db}
	router := gin.New()
	router.POST("/import", app.importCustomNodes)
	body := map[string]any{"source": `proxies:
  - name: WS
    type: trojan
    server: tr.example.com
    port: 443
    password: secret
    udp: true
    skip-cert-verify: true
    network: ws
    ws-opts:
      path: /ws
      headers:
        Host: cdn.example.com
`}
	payload, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/import", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("import status = %d body=%s", w.Code, w.Body.String())
	}
	var row model.CustomNode
	if err := db.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Link != "" || row.Protocol != "trojan" || row.Address != "tr.example.com" || row.Port != 443 {
		t.Fatalf("structured row = %+v", row)
	}
	var params map[string]any
	if err := json.Unmarshal(row.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params["insecure"] != true || params["udp"] != true || params["transport"] != "ws" || params["path"] != "/ws" || params["host"] != "cdn.example.com" {
		t.Fatalf("stored params = %#v", params)
	}
}

func TestRemoteSubscriptionSSRFValidation(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/sub",
		"http://10.0.0.1/sub",
		"http://169.254.169.254/latest/meta-data",
		"http://100.64.0.1/sub",
		"http://[::1]/sub",
		"http://[fd00::1]/sub",
		"http://localhost/sub",
		"http://user:pass@8.8.8.8/sub",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateRemoteSubscriptionURL(context.Background(), u); err == nil {
				t.Fatalf("unsafe URL accepted: %s", raw)
			}
		})
	}
	if err := requirePublicSubscriptionIPs([]net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("10.0.0.1")}); err == nil {
		t.Fatal("mixed public/private DNS result accepted")
	}
}

func TestPreviewRemoteURLIsFetchedNotParsedAsShareLink(t *testing.T) {
	result, fetched, err := parseCustomNodeImportRequest(context.Background(), customNodeImportReq{Source: "http://127.0.0.1:8080/sub"})
	if err == nil || fetched || len(result.Nodes) != 0 {
		t.Fatalf("loopback URL result=%+v fetched=%v err=%v", result, fetched, err)
	}
	if !strings.Contains(err.Error(), "内网") && !strings.Contains(err.Error(), "本机") {
		t.Fatalf("unexpected SSRF error: %v", err)
	}
}
