package agent

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// maxLogLines caps how much history the panel can pull in one request, so a
// noisy node can never flood the WebSocket.
const maxLogLines = 1000

// ReadLogs returns the most recent sing-box service logs from this node.
//
// journalctl is the source of truth for a systemd unit (the official install
// logs to the journal, not to a file). It is read-only and the unit name is
// fixed, so nothing here lets the panel run arbitrary commands.
func ReadLogs(ctx context.Context, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	if lines > maxLogLines {
		lines = maxLogLines
	}
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	// 优先尝试读取 journalctl 标准日志
	out, err := run(runCtx, "journalctl",
		"-u", ServiceName,
		"-n", strconv.Itoa(lines),
		"--no-pager",
	)
	if err == nil && strings.TrimSpace(out) != "" && !strings.Contains(out, "-- No entries --") {
		return out, nil
	}

	// 降级使用 systemctl status 获取包含 PID、内存及最新日志的列表
	statusOut, statusErr := run(runCtx, "systemctl", "status", ServiceName, "-n", strconv.Itoa(lines), "--no-pager")
	if statusErr == nil && strings.TrimSpace(statusOut) != "" {
		return statusOut, nil
	}

	if strings.TrimSpace(out) != "" {
		return out, nil
	}
	return "(暂无日志，请确认 sing-box 服务已在节点上安装并运行)", nil
}
