package agent

import (
	"context"
	"fmt"
	"os"
)

const (
	agentUninstallUnit     = "/etc/systemd/system/singbox-panel-agent-uninstall.service"
	agentUninstallScript   = "/run/singbox-panel-agent-uninstall.sh"
	agentUninstallUnitName = "singbox-panel-agent-uninstall.service"
	agentUninstallPIDFile  = "/run/singbox-panel-agent-uninstall.pid"
)

const selfUninstallScript = "#!/bin/sh\n" + agentTaskStartupScript + `sleep 5
if [ "$SERVICE_MANAGER" = systemd ]; then
  systemctl disable --now singbox-panel-agent.service >/dev/null 2>&1 || true
else
  rc-service singbox-panel-agent stop >/dev/null 2>&1 || true
  rc-update del singbox-panel-agent default >/dev/null 2>&1 || true
fi
rm -f /usr/local/bin/singbox-panel-agent
rm -rf /etc/singbox-panel-agent
rm -f /etc/systemd/system/singbox-panel-agent.service
rm -f /etc/systemd/system/singbox-panel-agent-uninstall.service
rm -f /etc/init.d/singbox-panel-agent
rm -f /run/singbox-panel-agent.pid
if [ "$SERVICE_MANAGER" = systemd ]; then
  systemctl daemon-reload >/dev/null 2>&1 || true
fi
rm -f /run/singbox-panel-agent-uninstall.sh
rm -f /run/singbox-panel-agent-uninstall.pid
`

const selfUninstallUnit = `[Unit]
Description=Remove SingBox Panel Agent

[Service]
Type=notify
NotifyAccess=all
TimeoutStartSec=10s
ExecStart=/bin/sh /run/singbox-panel-agent-uninstall.sh
`

// ScheduleSelfUninstall creates a detached cleanup process. It waits briefly so
// the command acknowledgement reaches the panel before stopping this Agent,
// then removes the binary, configuration and host service definition.
func ScheduleSelfUninstall(ctx context.Context) (string, error) {
	manager, err := currentServiceManager()
	if err != nil {
		return "", err
	}
	if err := writeAtomicFile(agentUninstallScript, []byte(selfUninstallScript), 0o700); err != nil {
		return "", fmt.Errorf("prepare Agent cleanup script: %w", err)
	}
	switch manager.Kind() {
	case serviceKindSystemd:
		if err := writeAtomicFile(agentUninstallUnit, []byte(selfUninstallUnit), 0o644); err != nil {
			_ = os.Remove(agentUninstallScript)
			return "", fmt.Errorf("prepare Agent cleanup service: %w", err)
		}
		if out, err := manager.Prepare(ctx); err != nil {
			cleanupAgentUninstallFiles()
			return out, fmt.Errorf("reload systemd for Agent cleanup: %w", err)
		}
		if out, err := startSystemdAgentTask(ctx, agentUninstallUnitName, run); err != nil {
			cleanupAgentUninstallFiles()
			_, _ = manager.Prepare(context.Background())
			return out, fmt.Errorf("start Agent cleanup: %w", err)
		}
	case serviceKindOpenRC:
		if err := launchOpenRCDetachedShell(ctx, agentUninstallScript, agentUninstallPIDFile); err != nil {
			cleanupAgentUninstallFiles()
			return "", err
		}
	default:
		cleanupAgentUninstallFiles()
		return "", fmt.Errorf("unsupported service manager %q", manager.Kind())
	}
	return "Agent、配置文件和开机自启服务将在数秒后删除", nil
}

func cleanupAgentUninstallFiles() {
	_ = os.Remove(agentUninstallUnit)
	_ = os.Remove(agentUninstallScript)
	_ = os.Remove(agentUninstallPIDFile)
}
