package agent

import (
	"context"
	"fmt"
	"strings"
)

// allowedServiceActions maps 1:1 to `systemctl <action> sing-box`.
var allowedServiceActions = map[string]bool{
	"start": true, "stop": true, "restart": true,
	"reload": true, "enable": true, "disable": true,
}

// ServiceAction runs a whitelisted systemctl action against the official unit.
func ServiceAction(ctx context.Context, action string) (string, error) {
	if action == "status" {
		return serviceStatusText(ctx), nil
	}
	if !allowedServiceActions[action] {
		return "", fmt.Errorf("disallowed service action %q", action)
	}
	// Pick up a unit file rewritten by an install/upgrade, so a restart really
	// starts the current definition instead of a stale cached one.
	if action == "start" || action == "restart" {
		_, _ = run(ctx, "systemctl", "daemon-reload")
	}
	return run(ctx, "systemctl", action, ServiceName)
}

// ServiceActive reports whether sing-box.service is active.
func ServiceActive(ctx context.Context) bool {
	out, _ := run(ctx, "systemctl", "is-active", ServiceName)
	return strings.TrimSpace(out) == "active"
}

// ServiceEnabled reports whether sing-box.service is enabled at boot.
func ServiceEnabled(ctx context.Context) bool {
	out, _ := run(ctx, "systemctl", "is-enabled", ServiceName)
	s := strings.TrimSpace(out)
	return s == "enabled" || s == "enabled-runtime" || s == "alias"
}

func serviceStatusText(ctx context.Context) string {
	out, _ := run(ctx, "systemctl", "status", ServiceName, "--no-pager", "-l")
	return out
}
