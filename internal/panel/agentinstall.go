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

	"github.com/gin-gonic/gin"
)

// agentInstallScript is served at /api/agent/install.sh. It installs the
// SingBox Panel agent (NOT sing-box) as its own systemd unit; the agent then
// installs/manages the official sing-box on the host.
const agentInstallScript = `#!/bin/sh
set -e
URL=""; TOKEN=""; INSECURE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --url) URL="$2"; shift 2 ;;
    --token) TOKEN="$2"; shift 2 ;;
    --insecure) INSECURE="true"; shift ;;
    *) shift ;;
  esac
done
[ -n "$URL" ] || { echo "missing --url"; exit 1; }
[ -n "$TOKEN" ] || { echo "missing --token"; exit 1; }
[ "$(id -u)" = "0" ] || { echo "must run as root"; exit 1; }

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) echo "unsupported arch: $ARCH"; exit 1 ;;
esac

BIN=/usr/local/bin/singbox-panel-agent
PREV=/usr/local/bin/singbox-panel-agent.prev
echo "downloading singbox-panel-agent ($GOARCH) ..."
# Download beside the target and rename into place: writing directly over a
# running executable fails with "Text file busy", and a rename is atomic, so
# re-running this script on a live node is a safe in-place upgrade.
TMP="$(mktemp /usr/local/bin/.singbox-panel-agent.XXXXXX)"
trap 'rm -f "$TMP"' EXIT
curl -fsSL "$URL/api/agent/download?arch=$GOARCH" -o "$TMP"
EXPECTED="$(curl -fsSL "$URL/api/agent/checksum?arch=$GOARCH" | tr -d '[:space:]')"
case "$EXPECTED" in
  *[!0-9a-fA-F]*|'') echo "invalid Agent checksum"; exit 1 ;;
esac
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
chmod +x "$TMP"
"$TMP" --version | grep -q '^singbox-panel-agent ' || { echo "invalid Agent binary"; exit 1; }
HAD_OLD=0
if [ -x "$BIN" ]; then
  cp -p "$BIN" "$PREV"
  HAD_OLD=1
fi
mv -f "$TMP" "$BIN"

mkdir -p /etc/singbox-panel-agent
cat > /etc/singbox-panel-agent/agent.conf <<EOF
URL=$URL
TOKEN=$TOKEN
INSECURE=$INSECURE
EOF
chmod 600 /etc/singbox-panel-agent/agent.conf

cat > /etc/systemd/system/singbox-panel-agent.service <<EOF
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

systemctl daemon-reload
systemctl enable singbox-panel-agent
# restart, not "enable --now": an already-running agent must be replaced by the
# freshly downloaded binary, which --now would leave untouched.
rm -f /run/singbox-panel-agent.ready
systemctl restart singbox-panel-agent || true
i=0
while [ "$i" -lt 30 ]; do
  if systemctl is-active --quiet singbox-panel-agent && [ -s /run/singbox-panel-agent.ready ]; then
    rm -f "$PREV"
    echo "singbox-panel-agent installed and connected."
    exit 0
  fi
  i=$((i + 1))
  sleep 2
done
echo "new Agent failed to connect; restoring previous binary" >&2
systemctl stop singbox-panel-agent >/dev/null 2>&1 || true
if [ "$HAD_OLD" = "1" ] && [ -x "$PREV" ]; then
  rm -f "$BIN"
  mv -f "$PREV" "$BIN"
  systemctl restart singbox-panel-agent >/dev/null 2>&1 || true
else
  rm -f "$BIN"
fi
exit 1
`

func (a *App) handleAgentInstallScript(c *gin.Context) {
	c.Data(http.StatusOK, "text/x-shellscript; charset=utf-8", []byte(agentInstallScript))
}

func (a *App) handleAgentDownload(c *gin.Context) {
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
	checksum, err := fileChecksum(path)
	if err != nil {
		c.String(http.StatusInternalServerError, "checksum agent binary: %v", err)
		return
	}
	c.Header("X-SingBox-Panel-SHA256", checksum)
	c.Header("Cache-Control", "no-store")
	c.FileAttachment(path, "singbox-panel-agent")
}

func (a *App) handleAgentChecksum(c *gin.Context) {
	arch := c.Query("arch")
	if arch != "amd64" && arch != "arm64" {
		c.String(http.StatusBadRequest, "unsupported arch %q", arch)
		return
	}
	path := filepath.Join(a.cfg.AgentsDir, "singbox-panel-agent-linux-"+arch)
	checksum, err := fileChecksum(path)
	if err != nil {
		if os.IsNotExist(err) {
			c.String(http.StatusNotFound, "agent binary not found")
			return
		}
		c.String(http.StatusInternalServerError, "checksum agent binary: %v", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.String(http.StatusOK, "%s\n", checksum)
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

// installCommand returns the one-line command an admin runs on a VPS.
func installCommand(baseURL, token string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(`curl -fsSL %s | sudo bash -s -- --url %s --token %s`,
		shellQuote(baseURL+"/api/agent/install.sh"), shellQuote(baseURL), shellQuote(token))
}

// shellQuote returns one POSIX-shell argument. The generated command is copied
// into an administrator's terminal, so configuration values must never be able
// to split the command or introduce shell syntax.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
