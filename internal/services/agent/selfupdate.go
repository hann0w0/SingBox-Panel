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
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// agentBinary is where the install script puts the agent.
	agentBinary         = "/usr/local/bin/singbox-panel-agent"
	agentPreviousBinary = "/usr/local/bin/singbox-panel-agent.prev"
	agentServiceName    = "singbox-panel-agent"

	agentReadyFile       = "/run/singbox-panel-agent.ready"
	agentExpectedSHAFile = "/run/singbox-panel-agent.expected-sha256"
	agentUpgradeUnit     = "/etc/systemd/system/singbox-panel-agent-upgrade.service"
	agentUpgradeScript   = "/run/singbox-panel-agent-upgrade.sh"
	agentUpgradeUnitName = "singbox-panel-agent-upgrade.service"
	agentUpgradePIDFile  = "/run/singbox-panel-agent-upgrade.pid"
	agentUpdateStatus    = "/var/lib/singbox-panel-agent/update-status"

	agentChecksumHeader = "X-SingBox-Panel-SHA256"
	maxAgentBinarySize  = int64(128 << 20)
)

const agentUpgradeScriptBody = "#!/bin/sh\n" + agentTaskStartupScript + `sleep 3
READY=/run/singbox-panel-agent.ready
EXPECTED=/run/singbox-panel-agent.expected-sha256
MY_EXPECTED_SHA="__EXPECTED_SHA__"
BIN=/usr/local/bin/singbox-panel-agent
PREV=/usr/local/bin/singbox-panel-agent.prev
STATUS=/var/lib/singbox-panel-agent/update-status
PIDFILE=/run/singbox-panel-agent-upgrade.pid

service_active() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl is-active --quiet singbox-panel-agent.service
  else
    rc-service singbox-panel-agent status >/dev/null 2>&1
  fi
}

service_restart() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl daemon-reload >/dev/null 2>&1 || true
    systemctl restart singbox-panel-agent.service
  else
    rc-service singbox-panel-agent restart
  fi
}

service_stop() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl stop singbox-panel-agent.service >/dev/null 2>&1 || true
  else
    rc-service singbox-panel-agent stop >/dev/null 2>&1 || true
  fi
}

owns_upgrade() {
  expected_sha=$(cat "$EXPECTED" 2>/dev/null || true)
  [ -n "$expected_sha" ] && [ "$expected_sha" = "$MY_EXPECTED_SHA" ]
}

mkdir -p /var/lib/singbox-panel-agent
if ! owns_upgrade; then
  exit 0
fi
rm -f "$READY"
service_restart >/dev/null 2>&1 || true

i=0
while [ "$i" -lt 30 ]; do
  if ! owns_upgrade; then
    exit 0
  fi
  expected_sha=$(cat "$EXPECTED" 2>/dev/null || true)
  ready_sha=$(sed -n '2p' "$READY" 2>/dev/null || true)
  if service_active && [ -n "$expected_sha" ] && [ "$ready_sha" = "$expected_sha" ]; then
    printf 'ok\n' > "$STATUS"
    rm -f "$PREV" "$EXPECTED"
    rm -f /etc/systemd/system/singbox-panel-agent-upgrade.service
    rm -f /run/singbox-panel-agent-upgrade.sh "$PIDFILE"
    if [ "$SERVICE_MANAGER" = systemd ]; then systemctl daemon-reload >/dev/null 2>&1 || true; fi
    exit 0
  fi
  i=$((i + 1))
  sleep 2
done

if ! owns_upgrade; then
  exit 0
fi
printf 'rollback\n' > "$STATUS"
service_stop
if [ -x "$PREV" ]; then
	if mv -f "$PREV" "$BIN" && chmod 755 "$BIN" && service_restart >/dev/null 2>&1; then
	  printf 'rollback-ok\n' > "$STATUS"
	else
	  printf 'rollback-failed\n' > "$STATUS"
	fi
else
  printf 'rollback-failed-no-previous-binary\n' > "$STATUS"
fi
rm -f /etc/systemd/system/singbox-panel-agent-upgrade.service
rm -f /run/singbox-panel-agent-upgrade.sh "$EXPECTED" "$PIDFILE"
if [ "$SERVICE_MANAGER" = systemd ]; then systemctl daemon-reload >/dev/null 2>&1 || true; fi
`

func renderAgentUpgradeScript(expectedSHA string) string {
	return strings.ReplaceAll(agentUpgradeScriptBody, "__EXPECTED_SHA__", expectedSHA)
}

const agentUpgradeUnitBody = `[Unit]
Description=Upgrade SingBox Panel Agent with automatic rollback

[Service]
Type=notify
NotifyAccess=all
TimeoutStartSec=10s
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
func SelfUpdate(ctx context.Context, panelURL, token string, insecure bool) (string, error) {
	base := httpBase(panelURL)
	if base == "" {
		return "", fmt.Errorf("panel URL 未知，无法升级")
	}
	u := fmt.Sprintf("%s/api/agent/download?arch=%s", base, url.QueryEscape(runtime.GOARCH))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
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
	if upgradeWatchdogActive(ctx) {
		return "", errors.New("已有 Agent 升级守护进程正在运行，请等待当前升级完成后重试")
	}

	if _, err := os.Stat(agentBinary); err != nil {
		return "", fmt.Errorf("当前 Agent 二进制不存在: %w", err)
	}
	_ = os.Remove(agentPreviousBinary)
	if err := os.Rename(agentBinary, agentPreviousBinary); err != nil {
		return "", fmt.Errorf("备份旧 Agent 失败: %w", err)
	}
	if err := os.Rename(tmpName, agentBinary); err != nil {
		if rollbackErr := os.Rename(agentPreviousBinary, agentBinary); rollbackErr != nil {
			return "", fmt.Errorf("替换 Agent 失败: %v；恢复旧 Agent 也失败: %w", err, rollbackErr)
		}
		return "", fmt.Errorf("替换 Agent 失败: %w", err)
	}
	if err := writeAtomicFile(agentExpectedSHAFile, []byte(actual+"\n"), 0o644); err != nil {
		rollbackErr := restorePreviousAgent()
		_ = os.Remove(agentExpectedSHAFile)
		if rollbackErr != nil {
			return "", fmt.Errorf("写入 Agent 就绪校验失败: %v；恢复旧 Agent 也失败: %w", err, rollbackErr)
		}
		return "", fmt.Errorf("写入 Agent 就绪校验失败，已恢复旧 Agent: %w", err)
	}
	if err := scheduleAgentUpgrade(ctx, actual); err != nil {
		_ = os.Remove(agentExpectedSHAFile)
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

func upgradeWatchdogActive(ctx context.Context) bool {
	if pidFileAlive(agentUpgradePIDFile) {
		return true
	}
	manager, err := currentServiceManager()
	if err != nil {
		return false
	}
	if manager.Kind() == serviceKindSystemd {
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if _, err := run(checkCtx, "systemctl", "is-active", "--quiet", agentUpgradeUnitName); err == nil {
			return true
		}
	}
	return false
}

func pidFileAlive(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func scheduleAgentUpgrade(ctx context.Context, expectedSHA string) error {
	manager, err := currentServiceManager()
	if err != nil {
		return err
	}
	if err := writeAtomicFile(agentUpgradeScript, []byte(renderAgentUpgradeScript(expectedSHA)), 0o700); err != nil {
		return fmt.Errorf("prepare Agent upgrade script: %w", err)
	}
	switch manager.Kind() {
	case serviceKindSystemd:
		if err := writeAtomicFile(agentUpgradeUnit, []byte(agentUpgradeUnitBody), 0o644); err != nil {
			_ = os.Remove(agentUpgradeScript)
			return fmt.Errorf("prepare Agent upgrade service: %w", err)
		}
		if out, err := manager.Prepare(ctx); err != nil {
			cleanupAgentUpgradeFiles()
			return fmt.Errorf("reload systemd for Agent upgrade: %w: %s", err, out)
		}
		if out, err := startSystemdAgentTask(ctx, agentUpgradeUnitName, run); err != nil {
			cleanupAgentUpgradeFiles()
			_, _ = manager.Prepare(context.Background())
			return fmt.Errorf("start Agent upgrade service: %w: %s", err, out)
		}
	case serviceKindOpenRC:
		if err := launchOpenRCDetachedShell(ctx, agentUpgradeScript, agentUpgradePIDFile); err != nil {
			cleanupAgentUpgradeFiles()
			return err
		}
	default:
		cleanupAgentUpgradeFiles()
		return fmt.Errorf("unsupported service manager %q", manager.Kind())
	}
	return nil
}

func cleanupAgentUpgradeFiles() {
	_ = os.Remove(agentUpgradeUnit)
	_ = os.Remove(agentUpgradeScript)
	_ = os.Remove(agentUpgradePIDFile)
}

func restorePreviousAgent() error {
	return restoreFileFromPrevious(agentPreviousBinary, agentBinary)
}

func restoreFileFromPrevious(previous, live string) error {
	return os.Rename(previous, live)
}

// markAgentReady is called only after the WebSocket handshake and registration
// write succeed. The marker includes the running executable's hash so a stale
// process cannot satisfy a watchdog waiting for a newly staged binary.
func markAgentReady(version string) error {
	executable, err := currentExecutablePath()
	if err != nil {
		return err
	}
	checksum, err := fileSHA256(executable)
	if err != nil {
		return fmt.Errorf("hash running Agent: %w", err)
	}
	return writeAtomicFile(agentReadyFile, []byte(readyMarker(version, checksum)), 0o644)
}

func readyMarker(version, checksum string) string {
	return version + "\n" + checksum + "\n"
}

func currentExecutablePath() (string, error) {
	// Opening /proc/self/exe follows the kernel's reference to the running
	// inode. This matters during an upgrade: the pathname may already point to
	// the new binary while the old process is still running from a deleted inode.
	if _, err := os.Stat("/proc/self/exe"); err == nil {
		return "/proc/self/exe", nil
	}
	return os.Executable()
}
