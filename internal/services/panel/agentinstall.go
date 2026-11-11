package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func (a *App) handleAgentInstallScript(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/x-shellscript; charset=utf-8", []byte(agentInstallScript))
}

const agentInstallCodeTTL = 10 * time.Minute

type agentInstallCode struct {
	ServerID uint
	Expires  time.Time
}

type agentArtifactChecksum struct {
	Size     int64
	ModTime  time.Time
	Checksum string
}

func (a *App) newAgentInstallCommand(serverID uint) string {
	var active int64
	if a.db == nil || a.db.Model(&model.Server{}).
		Where("id = ? AND agent_token <> ''", serverID).
		Count(&active).Error != nil || active != 1 {
		return ""
	}
	code := randHex(24)
	now := time.Now()
	a.agentInstallMu.Lock()
	if a.agentInstallCodes == nil {
		a.agentInstallCodes = make(map[string]agentInstallCode)
	}
	for existing, entry := range a.agentInstallCodes {
		if now.After(entry.Expires) {
			delete(a.agentInstallCodes, existing)
		}
	}
	if len(a.agentInstallCodes) >= 4096 {
		for existing := range a.agentInstallCodes {
			delete(a.agentInstallCodes, existing)
			if len(a.agentInstallCodes) < 4096 {
				break
			}
		}
	}
	a.agentInstallCodes[code] = agentInstallCode{ServerID: serverID, Expires: now.Add(agentInstallCodeTTL)}
	a.agentInstallMu.Unlock()
	return installCommand(a.baseURL(), code)
}

func (a *App) invalidateAgentInstallCodes(serverID uint) {
	a.agentInstallMu.Lock()
	defer a.agentInstallMu.Unlock()
	for code, entry := range a.agentInstallCodes {
		if entry.ServerID == serverID {
			delete(a.agentInstallCodes, code)
		}
	}
}

func (a *App) exchangeAgentInstallCode(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
	if err := c.Request.ParseForm(); err != nil {
		c.String(http.StatusBadRequest, "invalid registration request")
		return
	}
	code := strings.TrimSpace(c.PostForm("code"))
	now := time.Now()
	a.agentInstallMu.Lock()
	entry, ok := a.agentInstallCodes[code]
	if ok && (code == "" || now.After(entry.Expires)) {
		delete(a.agentInstallCodes, code)
		ok = false
	}
	a.agentInstallMu.Unlock()
	if !ok {
		c.String(http.StatusUnauthorized, "invalid or expired registration code")
		return
	}

	// Credential resets take the same per-server lock before invalidating install
	// codes. Recheck and consume the code only after acquiring that lock so an
	// exchange cannot return an obsolete token after a reset has completed.
	unlockOperation := a.lockServerOperation(entry.ServerID)
	defer unlockOperation()
	a.agentInstallMu.Lock()
	current, ok := a.agentInstallCodes[code]
	if ok && current == entry && !time.Now().After(current.Expires) {
		delete(a.agentInstallCodes, code)
	} else {
		ok = false
	}
	a.agentInstallMu.Unlock()
	if !ok {
		c.String(http.StatusUnauthorized, "invalid or expired registration code")
		return
	}

	var token string
	if err := a.db.Model(&model.Server{}).
		Where("id = ?", entry.ServerID).
		Pluck("agent_token", &token).Error; err != nil || token == "" {
		c.String(http.StatusUnauthorized, "registration target no longer exists")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(token))
}

func (a *App) authenticateAgentArtifact(c *gin.Context) bool {
	token := bearerToken(c)
	if token == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing agent token"})
		return false
	}
	var count int64
	if err := a.db.Model(&model.Server{}).
		Where("agent_token = ?", token).
		Count(&count).Error; err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid agent token"})
		return false
	}
	if count != 1 {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid agent token"})
		return false
	}
	return true
}

func (a *App) handleAgentDownload(c *gin.Context) {
	if !a.authenticateAgentArtifact(c) {
		return
	}
	arch := c.Query("arch")
	switch arch {
	case "amd64", "arm64":
	default:
		c.String(http.StatusBadRequest, "unsupported arch %q", arch)
		return
	}
	path := filepath.Join(a.cfg.AgentsDir, "singbox-panel-agent-linux-"+arch)
	if _, err := os.Stat(path); err != nil {
		c.String(http.StatusNotFound,
			"agent binary not found: %s\nBuild agents first: make agents (or GOOS=linux GOARCH=%s go build -o %s ./cmd/agent)",
			path, arch, path)
		return
	}
	checksum, err := a.cachedFileChecksum(path)
	if err != nil {
		c.String(http.StatusInternalServerError, "checksum agent binary: %v", err)
		return
	}
	c.Header("X-SingBox-Panel-SHA256", checksum)
	c.Header("ETag", `"sha256-`+checksum+`"`)
	c.Header("Cache-Control", "private, max-age=300, must-revalidate")
	c.FileAttachment(path, "singbox-panel-agent")
}

func (a *App) handleAgentChecksum(c *gin.Context) {
	if !a.authenticateAgentArtifact(c) {
		return
	}
	arch := c.Query("arch")
	if arch != "amd64" && arch != "arm64" {
		c.String(http.StatusBadRequest, "unsupported arch %q", arch)
		return
	}
	path := filepath.Join(a.cfg.AgentsDir, "singbox-panel-agent-linux-"+arch)
	checksum, err := a.cachedFileChecksum(path)
	if err != nil {
		if os.IsNotExist(err) {
			c.String(http.StatusNotFound, "agent binary not found")
			return
		}
		c.String(http.StatusInternalServerError, "checksum agent binary: %v", err)
		return
	}
	c.Header("Cache-Control", "private, max-age=300, must-revalidate")
	c.String(http.StatusOK, "%s\n", checksum)
}

func (a *App) cachedFileChecksum(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	a.agentArtifactMu.Lock()
	entry, ok := a.agentArtifacts[path]
	a.agentArtifactMu.Unlock()
	if ok && entry.Size == info.Size() && entry.ModTime.Equal(info.ModTime()) {
		return entry.Checksum, nil
	}
	checksum, err := fileChecksum(path)
	if err != nil {
		return "", err
	}
	a.agentArtifactMu.Lock()
	if a.agentArtifacts == nil {
		a.agentArtifacts = make(map[string]agentArtifactChecksum)
	}
	a.agentArtifacts[path] = agentArtifactChecksum{Size: info.Size(), ModTime: info.ModTime(), Checksum: checksum}
	a.agentArtifactMu.Unlock()
	return checksum, nil
}

func fileChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// installCommand returns a command containing only a short-lived, one-time
// registration code. The long-lived Agent token is exchanged over HTTPS and
// therefore never lands in shell history or the process argument list.
func installCommand(baseURL, code string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	scriptURL := shellQuote(baseURL + "/api/agent/install.sh")
	args := fmt.Sprintf("--url %s --code %s", shellQuote(baseURL), shellQuote(code))
	// One short line, as before OpenRC support. Fetching with wget and
	// re-executing under sudo/doas both happen inside the script, so neither
	// has to be repeated here three times just to cover Alpine and non-root
	// shells.
	return fmt.Sprintf(`{ curl -fsSL %s || wget -qO- %s; } | sh -s -- %s`,
		scriptURL, scriptURL, args)
}

// shellQuote returns one POSIX-shell argument. The generated command is copied
// into an administrator's terminal, so configuration values must never be able
// to split the command or introduce shell syntax.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
