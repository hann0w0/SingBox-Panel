package panel

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestAgentInstallScriptSyntaxAndTransactionalRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install-agent.sh")
	if err := os.WriteFile(path, []byte(agentInstallScript), 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("install script syntax: %v\n%s", err, output)
	}
	for _, required := range []string{
		"restore_target \"$BIN\"",
		"restore_target \"$CONFIG\"",
		"restore_target \"$UNIT\"",
		"TRANSACTION_STARTED=1",
		"COMMITTED=1",
	} {
		if !strings.Contains(agentInstallScript, required) {
			t.Fatalf("install script is missing transactional step %q", required)
		}
	}
}

func postAgentRegistrationCode(router http.Handler, code string) *httptest.ResponseRecorder {
	form := url.Values{"code": {code}}
	req := httptest.NewRequest(http.MethodPost, "/api/agent/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestAgentInstallCodeIsOneTimeAndExpires(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	server := model.Server{Name: "node", AgentToken: "0123456789abcdef0123456789abcdef"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	a := &App{db: db, agentInstallCodes: make(map[string]agentInstallCode)}
	router := gin.New()
	router.POST("/api/agent/register", a.exchangeAgentInstallCode)

	const validCode = "abcdef0123456789abcdef0123456789abcdef0123456789"
	a.agentInstallCodes[validCode] = agentInstallCode{ServerID: server.ID, Expires: time.Now().Add(time.Minute)}
	first := postAgentRegistrationCode(router, validCode)
	if first.Code != http.StatusOK || first.Body.String() != server.AgentToken {
		t.Fatalf("first exchange = %d %q", first.Code, first.Body.String())
	}
	second := postAgentRegistrationCode(router, validCode)
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("reused code status = %d", second.Code)
	}

	const expiredCode = "111111111111111111111111111111111111111111111111"
	a.agentInstallCodes[expiredCode] = agentInstallCode{ServerID: server.ID, Expires: time.Now().Add(-time.Second)}
	expired := postAgentRegistrationCode(router, expiredCode)
	if expired.Code != http.StatusUnauthorized {
		t.Fatalf("expired code status = %d", expired.Code)
	}
	if _, exists := a.agentInstallCodes[expiredCode]; exists {
		t.Fatal("expired code was not consumed")
	}
}

func TestAgentInstallCommandNeverContainsLongLivedToken(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "node", AgentToken: "0123456789abcdef0123456789abcdef"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	a := &App{db: db, cfg: config.PanelConfig{BaseURL: "https://panel.example.com"}, agentInstallCodes: make(map[string]agentInstallCode)}
	command := a.newAgentInstallCommand(server.ID)
	if strings.Contains(command, server.AgentToken) {
		t.Fatal("install command exposed the long-lived Agent token")
	}
	if !strings.Contains(command, "--code") || len(a.agentInstallCodes) != 1 {
		t.Fatalf("install command did not mint a one-time code: %s", command)
	}
}

func agentArtifactAuthStatus(a *App, remoteIP, token string) int {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodGet, "/api/agent/download?arch=amd64", nil)
	req.RemoteAddr = net.JoinHostPort(remoteIP, "12345")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	ctx.Request = req
	if a.authenticateAgentArtifact(ctx) {
		return http.StatusNoContent
	}
	return recorder.Code
}

func TestAgentArtifactAuthenticationRequiresFixedToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	const (
		activeToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		agentIP     = "198.51.100.10"
	)
	otherToken := strings.Repeat("b", 64)
	server := model.Server{Name: "node", AgentToken: activeToken, PublicIP: agentIP}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	a := &App{db: db}

	if status := agentArtifactAuthStatus(a, agentIP, activeToken); status != http.StatusNoContent {
		t.Fatalf("active token status = %d", status)
	}
	for name, token := range map[string]string{
		"different token": otherToken,
		"missing token":   "",
		"unknown token":   "not-a-real-agent-token",
	} {
		t.Run(name, func(t *testing.T) {
			if status := agentArtifactAuthStatus(a, agentIP, token); status != http.StatusUnauthorized {
				t.Fatalf("status = %d", status)
			}
		})
	}
}
