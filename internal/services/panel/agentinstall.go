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

// agentInstallScript is served at /api/agent/install.sh. It installs the
// SingBox Panel agent (NOT sing-box) as its own systemd unit; the agent then
// installs/manages the official sing-box on the host.
const agentInstallScript = `#!/bin/sh
set -eu

URL=""; TOKEN=""; CODE=""; TMP=""; AUTH_CONFIG=""
BIN=/usr/local/bin/singbox-panel-agent
PREV=/usr/local/bin/singbox-panel-agent.prev
CONFIG=/etc/singbox-panel-agent/agent.conf
UNIT=/etc/systemd/system/singbox-panel-agent.service
BACKUP_DIR=""; CONFIG_TMP=""; UNIT_TMP=""; RESTORE_TMP=""
TRANSACTION_STARTED=0; COMMITTED=0
HAD_BIN=0; HAD_CONFIG=0; HAD_UNIT=0; WAS_ACTIVE=0; WAS_ENABLED=0

restore_target() {
  target="$1"; backup="$2"; existed="$3"
  if [ "$existed" = "1" ]; then
    RESTORE_TMP="$(mktemp "$(dirname "$target")/.singbox-panel-restore.XXXXXX")" || return 1
    cp -p "$backup" "$RESTORE_TMP" || return 1
    mv -f "$RESTORE_TMP" "$target" || return 1
    RESTORE_TMP=""
  else
    rm -f "$target" || return 1
  fi
}

cleanup() {
  status=$?
  trap - EXIT HUP INT TERM
  set +e
  if [ "$TRANSACTION_STARTED" = "1" ] && [ "$COMMITTED" != "1" ]; then
    echo "Agent installation failed; restoring the previous binary, configuration and service" >&2
    systemctl stop singbox-panel-agent >/dev/null 2>&1 || true
    rollback_failed=0
    restore_target "$BIN" "$BACKUP_DIR/bin" "$HAD_BIN" || rollback_failed=1
    restore_target "$CONFIG" "$BACKUP_DIR/config" "$HAD_CONFIG" || rollback_failed=1
    restore_target "$UNIT" "$BACKUP_DIR/unit" "$HAD_UNIT" || rollback_failed=1
    systemctl daemon-reload >/dev/null 2>&1 || rollback_failed=1
    if [ "$WAS_ENABLED" = "1" ]; then
      systemctl enable singbox-panel-agent >/dev/null 2>&1 || rollback_failed=1
    else
      systemctl disable singbox-panel-agent >/dev/null 2>&1 || true
    fi
    if [ "$WAS_ACTIVE" = "1" ]; then
      systemctl restart singbox-panel-agent >/dev/null 2>&1 || rollback_failed=1
    fi
    if [ "$rollback_failed" = "1" ]; then
      echo "automatic Agent rollback was incomplete; inspect $BACKUP_DIR" >&2
      BACKUP_DIR=""
    fi
  fi
  [ -z "$TMP" ] || rm -f "$TMP"
  [ -z "$CONFIG_TMP" ] || rm -f "$CONFIG_TMP"
  [ -z "$UNIT_TMP" ] || rm -f "$UNIT_TMP"
  [ -z "$RESTORE_TMP" ] || rm -f "$RESTORE_TMP"
  [ -z "$AUTH_CONFIG" ] || rm -f "$AUTH_CONFIG"
  [ -z "$BACKUP_DIR" ] || rm -rf "$BACKUP_DIR"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

while [ $# -gt 0 ]; do
  case "$1" in
    --url|--token|--code)
      [ $# -ge 2 ] && [ -n "$2" ] || { echo "$1 requires a value"; exit 1; }
      case "$1" in
        --url) URL="$2" ;;
        --token) TOKEN="$2" ;;
        --code) CODE="$2" ;;
      esac
      shift 2
      ;;
    --insecure) echo "--insecure is disabled in production"; exit 1 ;;
    *) echo "unknown option: $1"; exit 1 ;;
  esac
done
[ "$(id -u)" = "0" ] || { echo "must run as root"; exit 1; }
case "$URL" in
  https://*) ;;
  *) echo "--url must be a non-empty HTTPS URL"; exit 1 ;;
esac
case "$URL" in
  *[[:space:]]*) echo "--url must not contain whitespace"; exit 1 ;;
esac

if [ -z "$TOKEN" ]; then
  [ -n "$CODE" ] || { echo "missing one-time registration code"; exit 1; }
  TOKEN="$(curl -fsS -X POST --data-urlencode "code=$CODE" "$URL/api/agent/register")" || {
    echo "registration code exchange failed"; exit 1;
  }
fi
case "$TOKEN" in
  *[!0-9a-fA-F]*|'') echo "panel returned an invalid Agent token"; exit 1 ;;
esac
[ "${#TOKEN}" -ge 32 ] && [ "${#TOKEN}" -le 128 ] || {
  echo "panel returned an invalid Agent token length"; exit 1;
}

# Keep the long-lived token out of curl's argv (and therefore /proc/*/cmdline).
AUTH_CONFIG="$(mktemp /tmp/.singbox-panel-agent-auth.XXXXXX)"
chmod 600 "$AUTH_CONFIG"
printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" > "$AUTH_CONFIG"

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) echo "unsupported arch: $ARCH"; exit 1 ;;
esac

echo "downloading singbox-panel-agent ($GOARCH) ..."
TMP="$(mktemp /usr/local/bin/.singbox-panel-agent.XXXXXX)"
curl -fsSL --config "$AUTH_CONFIG" "$URL/api/agent/download?arch=$GOARCH" -o "$TMP"
EXPECTED="$(curl -fsSL --config "$AUTH_CONFIG" "$URL/api/agent/checksum?arch=$GOARCH" | tr -d '[:space:]')"
case "$EXPECTED" in
  *[!0-9a-fA-F]*|'') echo "invalid Agent checksum"; exit 1 ;;
esac
[ "${#EXPECTED}" = "64" ] || { echo "invalid Agent checksum length"; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL="$(sha256sum "$TMP" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL="$(shasum -a 256 "$TMP" | awk '{print $1}')"
elif command -v openssl >/dev/null 2>&1; then
  ACTUAL="$(openssl dgst -sha256 "$TMP" | awk '{print $NF}')"
else
  echo "sha256sum, shasum or openssl is required"; exit 1
fi
[ "$(printf '%s' "$ACTUAL" | tr 'A-F' 'a-f')" = "$(printf '%s' "$EXPECTED" | tr 'A-F' 'a-f')" ] || {
  echo "Agent SHA256 mismatch"; exit 1;
}
chmod 755 "$TMP"
"$TMP" --version | grep -q '^singbox-panel-agent ' || { echo "invalid Agent binary"; exit 1; }

mkdir -p /etc/singbox-panel-agent /var/lib/singbox-panel-agent
BACKUP_DIR="$(mktemp -d /var/lib/singbox-panel-agent/.install-rollback.XXXXXX)"
chmod 700 "$BACKUP_DIR"
if [ -f "$BIN" ]; then cp -p "$BIN" "$BACKUP_DIR/bin"; HAD_BIN=1; fi
if [ -f "$CONFIG" ]; then cp -p "$CONFIG" "$BACKUP_DIR/config"; HAD_CONFIG=1; fi
if [ -f "$UNIT" ]; then cp -p "$UNIT" "$BACKUP_DIR/unit"; HAD_UNIT=1; fi
if systemctl is-active --quiet singbox-panel-agent; then WAS_ACTIVE=1; fi
if systemctl is-enabled --quiet singbox-panel-agent; then WAS_ENABLED=1; fi
TRANSACTION_STARTED=1

mv -f "$TMP" "$BIN"
TMP=""

CONFIG_TMP="$(mktemp /etc/singbox-panel-agent/.agent.conf.XXXXXX)"
umask 077
printf 'URL=%s\nTOKEN=%s\n' "$URL" "$TOKEN" > "$CONFIG_TMP"
chmod 600 "$CONFIG_TMP"
mv -f "$CONFIG_TMP" "$CONFIG"
CONFIG_TMP=""

UNIT_TMP="$(mktemp /etc/systemd/system/.singbox-panel-agent.service.XXXXXX)"
cat > "$UNIT_TMP" <<EOF
[Unit]
Description=SingBox Panel Agent
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
ExecStart=/usr/local/bin/singbox-panel-agent --config /etc/singbox-panel-agent/agent.conf
Restart=always
RestartSec=5s
LimitNOFILE=infinity

[Install]
WantedBy=multi-user.target
EOF
chmod 644 "$UNIT_TMP"
mv -f "$UNIT_TMP" "$UNIT"
UNIT_TMP=""

systemctl daemon-reload
systemctl enable singbox-panel-agent
rm -f /run/singbox-panel-agent.ready
systemctl restart singbox-panel-agent
i=0
while [ "$i" -lt 30 ]; do
  if systemctl is-active --quiet singbox-panel-agent && [ -s /run/singbox-panel-agent.ready ]; then
    COMMITTED=1
    rm -f "$PREV"
    rm -rf "$BACKUP_DIR"
    BACKUP_DIR=""
    echo "singbox-panel-agent installed and connected."
    exit 0
  fi
  i=$((i + 1))
  sleep 2
done
echo "new Agent failed to connect" >&2
exit 1
`

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
	return fmt.Sprintf(`curl -fsSL %s | sudo bash -s -- --url %s --code %s`,
		shellQuote(baseURL+"/api/agent/install.sh"), shellQuote(baseURL), shellQuote(code))
}

// shellQuote returns one POSIX-shell argument. The generated command is copied
// into an administrator's terminal, so configuration values must never be able
// to split the command or introduce shell syntax.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
