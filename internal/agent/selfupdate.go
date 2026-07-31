package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// agentBinary is where the install script puts the agent.
	agentBinary         = "/usr/local/bin/singbox-panel-agent"
	agentPreviousBinary = "/usr/local/bin/singbox-panel-agent.prev"
	agentServiceName    = "singbox-panel-agent"

	agentReadyFile       = "/run/singbox-panel-agent.ready"
	agentUpgradeUnit     = "/etc/systemd/system/singbox-panel-agent-upgrade.service"
	agentUpgradeScript   = "/run/singbox-panel-agent-upgrade.sh"
	agentUpgradeUnitName = "singbox-panel-agent-upgrade.service"
	agentUpdateStatus    = "/var/lib/singbox-panel-agent/update-status"

	agentChecksumHeader = "X-SingBox-Panel-SHA256"
	maxAgentBinarySize  = int64(128 << 20)
)

const agentUpgradeScriptBody = `#!/bin/sh
sleep 3
READY=/run/singbox-panel-agent.ready
BIN=/usr/local/bin/singbox-panel-agent
PREV=/usr/local/bin/singbox-panel-agent.prev
STATUS=/var/lib/singbox-panel-agent/update-status
mkdir -p /var/lib/singbox-panel-agent
rm -f "$READY"
systemctl daemon-reload >/dev/null 2>&1 || true
systemctl restart singbox-panel-agent.service >/dev/null 2>&1 || true

i=0
while [ "$i" -lt 30 ]; do
  if systemctl is-active --quiet singbox-panel-agent.service && [ -s "$READY" ]; then
    printf 'ok\n' > "$STATUS"
    rm -f "$PREV"
    rm -f /etc/systemd/system/singbox-panel-agent-upgrade.service
    rm -f /run/singbox-panel-agent-upgrade.sh
    systemctl daemon-reload >/dev/null 2>&1 || true
    exit 0
  fi
  i=$((i + 1))
  sleep 2
done

printf 'rollback\n' > "$STATUS"
systemctl stop singbox-panel-agent.service >/dev/null 2>&1 || true
if [ -x "$PREV" ]; then
  rm -f "$BIN"
  mv -f "$PREV" "$BIN"
  chmod 755 "$BIN"
  systemctl restart singbox-panel-agent.service >/dev/null 2>&1 || true
else
  printf 'rollback-failed-no-previous-binary\n' > "$STATUS"
fi
rm -f /etc/systemd/system/singbox-panel-agent-upgrade.service
rm -f /run/singbox-panel-agent-upgrade.sh
systemctl daemon-reload >/dev/null 2>&1 || true
`

const agentUpgradeUnitBody = `[Unit]
Description=Upgrade SingBox Panel Agent with automatic rollback

[Service]
Type=oneshot
ExecStart=/bin/sh /run/singbox-panel-agent-upgrade.sh
`

// httpBase converts the panel's websocket URL into an http(s) base URL.
func httpBase(panelURL string) string {
	s := panelURL
	s = strings.TrimSuffix(s, "/api/agent/ws")
	s = strings.TrimSuffix(s, "/")
	if strings.HasPrefix(s, "wss://") {
		return "https://" + strings.TrimPrefix(s, "wss://")
	}
	if strings.HasPrefix(s, "ws://") {
		return "http://" + strings.TrimPrefix(s, "ws://")
	}
	return s
}

// SelfUpdate downloads and validates the Agent currently served by the panel,
// atomically stages it beside a previous binary, then starts a separate
// systemd watchdog. The watchdog only accepts the upgrade after the new Agent
// reconnects and writes its readiness marker; otherwise it restores the old
// binary automatically.
func SelfUpdate(ctx context.Context, panelURL string, insecure bool) (string, error) {
	base := httpBase(panelURL)
	if base == "" {
		return "", fmt.Errorf("panel URL 未知，无法升级")
	}
	u := fmt.Sprintf("%s/api/agent/download?arch=%s", base, url.QueryEscape(runtime.GOARCH))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	client := &http.Client{Timeout: 3 * time.Minute, Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载 Agent 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 Agent 失败: HTTP %d", resp.StatusCode)
	}
	expected, err := parseAgentChecksum(resp.Header.Get(agentChecksumHeader))
	if err != nil {
		return "", fmt.Errorf("Agent 校验信息无效: %w", err)
	}

	dir := filepath.Dir(agentBinary)
	tmp, err := os.CreateTemp(dir, ".singbox-panel-agent-*")
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxAgentBinarySize+1))
	if copyErr == nil {
		copyErr = tmp.Sync()
	}
	if closeErr := tmp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return "", fmt.Errorf("写入 Agent 失败: %w", copyErr)
	}
	if n > maxAgentBinarySize {
		return "", fmt.Errorf("下载的 Agent 超过大小限制 (%d 字节)", n)
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return "", fmt.Errorf("Agent SHA256 不匹配")
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return "", fmt.Errorf("设置 Agent 权限失败: %w", err)
	}
	version, err := validateAgentBinary(ctx, tmpName)
	if err != nil {
		return "", err
	}
	if current, err := fileSHA256(agentBinary); err == nil && current == actual {
		return fmt.Sprintf("Agent 已是最新版本（%s）", version), nil
	}

	if _, err := os.Stat(agentBinary); err != nil {
		return "", fmt.Errorf("当前 Agent 二进制不存在: %w", err)
	}
	_ = os.Remove(agentPreviousBinary)
	if err := os.Rename(agentBinary, agentPreviousBinary); err != nil {
		return "", fmt.Errorf("备份旧 Agent 失败: %w", err)
	}
	if err := os.Rename(tmpName, agentBinary); err != nil {
		_ = os.Rename(agentPreviousBinary, agentBinary)
		return "", fmt.Errorf("替换 Agent 失败: %w", err)
	}
	if err := scheduleAgentUpgrade(ctx); err != nil {
		rollbackErr := restorePreviousAgent()
		if rollbackErr != nil {
			return "", fmt.Errorf("启动升级守护失败: %v；恢复旧 Agent 失败: %w", err, rollbackErr)
		}
		return "", fmt.Errorf("启动升级守护失败，已恢复旧 Agent: %w", err)
	}
	return fmt.Sprintf("新 Agent 已校验（%s，%d 字节），将重启并在连接失败时自动回滚", version, n), nil
}

func parseAgentChecksum(raw string) (string, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	b, err := hex.DecodeString(raw)
	if err != nil || len(b) != sha256.Size {
		return "", errors.New("缺少有效的 SHA256 响应头")
	}
	return raw, nil
}

func fileSHA256(path string) (string, error) {
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

func validateAgentBinary(ctx context.Context, path string) (string, error) {
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := run(checkCtx, path, "--version")
	if err != nil {
		return "", fmt.Errorf("新 Agent 无法执行版本检查: %w: %s", err, out)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "singbox-panel-agent ") {
		return "", fmt.Errorf("新 Agent 版本输出异常: %q", firstLine(out))
	}
	return strings.TrimSpace(strings.TrimPrefix(firstLine(out), "singbox-panel-agent ")), nil
}

func scheduleAgentUpgrade(ctx context.Context) error {
	if err := writeAtomicFile(agentUpgradeScript, []byte(agentUpgradeScriptBody), 0o700); err != nil {
		return fmt.Errorf("prepare Agent upgrade script: %w", err)
	}
	if err := writeAtomicFile(agentUpgradeUnit, []byte(agentUpgradeUnitBody), 0o644); err != nil {
		_ = os.Remove(agentUpgradeScript)
		return fmt.Errorf("prepare Agent upgrade service: %w", err)
	}
	if out, err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		cleanupAgentUpgradeFiles()
		return fmt.Errorf("reload systemd for Agent upgrade: %w: %s", err, out)
	}
	if out, err := run(ctx, "systemctl", "--no-block", "start", agentUpgradeUnitName); err != nil {
		cleanupAgentUpgradeFiles()
		_, _ = run(context.Background(), "systemctl", "daemon-reload")
		return fmt.Errorf("start Agent upgrade service: %w: %s", err, out)
	}
	return nil
}

func cleanupAgentUpgradeFiles() {
	_ = os.Remove(agentUpgradeUnit)
	_ = os.Remove(agentUpgradeScript)
}

func restorePreviousAgent() error {
	_ = os.Remove(agentBinary)
	return os.Rename(agentPreviousBinary, agentBinary)
}

// markAgentReady is called only after the WebSocket handshake and registration
// write succeed. A separate updater process watches this marker and rolls back
// a binary that starts but cannot reconnect to its panel.
func markAgentReady(version string) error {
	return writeAtomicFile(agentReadyFile, []byte(version+"\n"), 0o644)
}
