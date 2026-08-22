package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/hann0w0/singbox-panel/internal/protocol"
)

// Official sing-box paths and unit name (see docs/SINGBOX-OFFICIAL.md).
const (
	SingboxBinary = "/usr/bin/sing-box"
	ConfigDir     = "/etc/sing-box"
	ConfigFile    = "/etc/sing-box/config.json"
	ServiceName   = "sing-box"
)

// singboxBinary returns the path to the sing-box executable. The official
// install lives at SingboxBinary, but a server may already carry a
// manually-installed binary elsewhere (commonly /usr/local/bin) — resolve those
// too so a pre-existing sing-box is still detected and usable. Falls back to the
// official path (the install target) when nothing is found.
func singboxBinary() string {
	candidates := []string{
		SingboxBinary,
		"/usr/local/bin/sing-box",
		"/usr/sbin/sing-box",
		"/usr/local/sbin/sing-box",
		"/opt/sing-box/sing-box",
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("sing-box"); err == nil {
		return p
	}
	return SingboxBinary
}

var versionRe = regexp.MustCompile(`^[0-9][0-9A-Za-z.\-]*$`)

// InstallSingbox installs the official sing-box using the requested method
// (script | apt | dnf) and channel (stable | beta). It only ever invokes the
// official installer/repositories documented by upstream.
func InstallSingbox(ctx context.Context, channel, version, method string) (string, error) {
	if channel == "" {
		channel = protocol.ChannelBeta
	}
	if channel != protocol.ChannelStable && channel != protocol.ChannelBeta {
		return "", fmt.Errorf("unknown sing-box channel %q", channel)
	}
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

// DetectVersion reports whether the sing-box binary is present and its version.
func DetectVersion(ctx context.Context) (installed bool, version string) {
	bin := singboxBinary()
	if _, err := os.Stat(bin); err != nil {
		return false, ""
	}
	if out, err := run(ctx, bin, "version", "-n"); err == nil && out != "" {
		return true, firstLine(out)
	}
	// Fallback for builds without `-n`: parse `sing-box version <X> ...`.
	if out, err := run(ctx, bin, "version"); err == nil {
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
