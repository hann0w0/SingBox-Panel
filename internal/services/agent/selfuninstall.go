package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

const (
	agentUninstallUnit     = "/etc/systemd/system/singbox-panel-agent-uninstall.service"
	agentUninstallScript   = "/run/singbox-panel-agent-uninstall.sh"
	agentUninstallUnitName = "singbox-panel-agent-uninstall.service"
)

const selfUninstallScript = `#!/bin/sh
sleep 5
systemctl disable --now singbox-panel-agent.service >/dev/null 2>&1 || true
rm -f /usr/local/bin/singbox-panel-agent
rm -rf /etc/singbox-panel-agent
rm -f /etc/systemd/system/singbox-panel-agent.service
rm -f /etc/systemd/system/singbox-panel-agent-uninstall.service
systemctl daemon-reload >/dev/null 2>&1 || true
rm -f /run/singbox-panel-agent-uninstall.sh
`

const selfUninstallUnit = `[Unit]
Description=Remove SingBox Panel Agent

[Service]
Type=oneshot
ExecStart=/bin/sh /run/singbox-panel-agent-uninstall.sh
`

// writeAtomicFile prevents a partial cleanup script or unit from being
// observed if the disk fills or the Agent is interrupted during preparation.
func writeAtomicFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".singbox-panel-uninstall-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// ScheduleSelfUninstall creates a separate systemd oneshot service. It waits
// briefly so the command acknowledgement reaches the panel before stopping
// this Agent, then removes the binary, configuration and both systemd units.
func ScheduleSelfUninstall(ctx context.Context) (string, error) {
	if err := writeAtomicFile(agentUninstallScript, []byte(selfUninstallScript), 0o700); err != nil {
		return "", fmt.Errorf("prepare Agent cleanup script: %w", err)
	}
	if err := writeAtomicFile(agentUninstallUnit, []byte(selfUninstallUnit), 0o644); err != nil {
		_ = os.Remove(agentUninstallScript)
		return "", fmt.Errorf("prepare Agent cleanup service: %w", err)
	}
	if out, err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		_ = os.Remove(agentUninstallUnit)
		_ = os.Remove(agentUninstallScript)
		return out, fmt.Errorf("reload systemd for Agent cleanup: %w", err)
	}
	if out, err := run(ctx, "systemctl", "--no-block", "start", agentUninstallUnitName); err != nil {
		_ = os.Remove(agentUninstallUnit)
		_ = os.Remove(agentUninstallScript)
		_, _ = run(context.Background(), "systemctl", "daemon-reload")
		return out, fmt.Errorf("start Agent cleanup: %w", err)
	}
	return "Agent、配置文件和开机自启服务将在数秒后删除", nil
}
