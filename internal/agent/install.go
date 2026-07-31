package agent

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/singpanel/singpanel/internal/protocol"
)

// Official sing-box paths and unit name (see docs/SINGBOX-OFFICIAL.md).
const (
	SingboxBinary = "/usr/bin/sing-box"
	ConfigDir     = "/etc/sing-box"
	ConfigFile    = "/etc/sing-box/config.json"
	ServiceName   = "sing-box"
)

var versionRe = regexp.MustCompile(`^[0-9][0-9A-Za-z.\-]*$`)

// InstallSingbox installs the official sing-box using the requested method
// (script | apt | dnf) and channel (stable | beta). It only ever invokes the
// official installer/repositories documented by upstream.
func InstallSingbox(ctx context.Context, channel, version, method string) (string, error) {
	if version != "" && !versionRe.MatchString(version) {
		return "", fmt.Errorf("invalid version %q", version)
	}
	switch method {
	case "apt":
		return installAPT(ctx, channel)
	case "dnf":
		return installDNF(ctx, channel)
	case "script", "":
		return installScript(ctx, channel, version)
	default:
		return "", fmt.Errorf("unknown install method %q", method)
	}
}

func installScript(ctx context.Context, channel, version string) (string, error) {
	var flags []string
	if channel == protocol.ChannelBeta {
		flags = append(flags, "--beta")
	}
	if version != "" {
		flags = append(flags, "--version", version)
	}
	script := "curl -fsSL https://sing-box.app/install.sh | sh"
	if len(flags) > 0 {
		script += " -s -- " + strings.Join(flags, " ")
	}
	return runShell(ctx, script)
}

func pkgName(channel string) string {
	if channel == protocol.ChannelBeta {
		return "sing-box-beta"
	}
	return "sing-box"
}

func installAPT(ctx context.Context, channel string) (string, error) {
	script := `set -e
mkdir -p /etc/apt/keyrings
curl -fsSL https://sing-box.app/gpg.key -o /etc/apt/keyrings/sagernet.asc
chmod a+r /etc/apt/keyrings/sagernet.asc
printf '%s\n' 'Types: deb' 'URIs: https://deb.sagernet.org/' 'Suites: *' 'Components: *' 'Enabled: yes' 'Signed-By: /etc/apt/keyrings/sagernet.asc' > /etc/apt/sources.list.d/sagernet.sources
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y ` + pkgName(channel)
	return runShell(ctx, script)
}

func installDNF(ctx context.Context, channel string) (string, error) {
	script := `set -e
dnf config-manager addrepo --from-repofile=https://sing-box.app/sing-box.repo 2>/dev/null || dnf config-manager --add-repo https://sing-box.app/sing-box.repo
dnf install -y ` + pkgName(channel)
	return runShell(ctx, script)
}

// DetectVersion reports whether the official binary is present and its version.
func DetectVersion(ctx context.Context) (installed bool, version string) {
	if _, err := os.Stat(SingboxBinary); err != nil {
		return false, ""
	}
	if out, err := run(ctx, SingboxBinary, "version", "-n"); err == nil && out != "" {
		return true, firstLine(out)
	}
	// Fallback for builds without `-n`: parse `sing-box version <X> ...`.
	if out, err := run(ctx, SingboxBinary, "version"); err == nil {
		fields := strings.Fields(firstLine(out))
		if len(fields) >= 3 && fields[0] == "sing-box" && fields[1] == "version" {
			return true, fields[2]
		}
		return true, firstLine(out)
	}
	return true, ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
