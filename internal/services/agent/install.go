package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

// Official sing-box paths and systemd unit name.
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
	installer := singboxInstaller{run: run, detectVersion: DetectVersion}
	return installer.install(ctx, channel, version, method)
}

type singboxInstaller struct {
	run           func(context.Context, string, ...string) (string, error)
	detectVersion func(context.Context) (bool, string)
}

func (i singboxInstaller) install(ctx context.Context, channel, version, method string) (string, error) {
	if channel == "" {
		channel = protocol.ChannelBeta
	}
	if channel != protocol.ChannelStable && channel != protocol.ChannelBeta {
		return "", fmt.Errorf("unknown sing-box channel %q", channel)
	}
	if version != "" && !versionRe.MatchString(version) {
		return "", fmt.Errorf("invalid version %q", version)
	}
	var output string
	var err error
	switch method {
	case "apt":
		output, err = i.installAPT(ctx, channel)
	case "dnf":
		output, err = i.installDNF(ctx, channel)
	case "script", "":
		output, err = i.installScript(ctx, channel, version)
	default:
		return "", fmt.Errorf("unknown install method %q", method)
	}
	if err != nil {
		return output, err
	}
	if err := ctx.Err(); err != nil {
		return output, err
	}
	installed, actualVersion := i.detectVersion(ctx)
	if err := ctx.Err(); err != nil {
		return output, err
	}
	if !installed || !versionRe.MatchString(actualVersion) {
		return output, fmt.Errorf("sing-box installation did not produce a working binary with a valid version")
	}
	if version != "" && actualVersion != version {
		return output, fmt.Errorf("sing-box version mismatch: requested %s, installed %s", version, actualVersion)
	}
	return output, nil
}

func (i singboxInstaller) installScript(ctx context.Context, channel, version string) (string, error) {
	file, err := os.CreateTemp("", "singbox-install-*.sh")
	if err != nil {
		return "", fmt.Errorf("create installer file: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Close(); err != nil {
		return "", err
	}
	output, err := i.run(ctx, "curl", "-fsSL", "--output", path, "https://sing-box.app/install.sh")
	if err != nil {
		return output, fmt.Errorf("download sing-box installer: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return output, fmt.Errorf("read sing-box installer: %w", err)
	}
	if info.Size() == 0 {
		return output, fmt.Errorf("downloaded sing-box installer is empty")
	}
	args := []string{path}
	if channel == protocol.ChannelBeta {
		args = append(args, "--beta")
	}
	if version != "" {
		args = append(args, "--version", version)
	}
	installedOutput, err := i.run(ctx, "sh", args...)
	if installedOutput != "" {
		if output != "" {
			output += "\n"
		}
		output += installedOutput
	}
	if err != nil {
		return output, fmt.Errorf("run sing-box installer: %w", err)
	}
	return output, nil
}

func pkgName(channel string) string {
	if channel == protocol.ChannelBeta {
		return "sing-box-beta"
	}
	return "sing-box"
}

func (i singboxInstaller) installAPT(ctx context.Context, channel string) (string, error) {
	script := `set -e
mkdir -p /etc/apt/keyrings
curl -fsSL https://sing-box.app/gpg.key -o /etc/apt/keyrings/sagernet.asc
chmod a+r /etc/apt/keyrings/sagernet.asc
printf '%s\n' 'Types: deb' 'URIs: https://deb.sagernet.org/' 'Suites: *' 'Components: *' 'Enabled: yes' 'Signed-By: /etc/apt/keyrings/sagernet.asc' > /etc/apt/sources.list.d/sagernet.sources
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y ` + pkgName(channel)
	return i.run(ctx, "sh", "-c", script)
}

func (i singboxInstaller) installDNF(ctx context.Context, channel string) (string, error) {
	script := `set -e
dnf config-manager addrepo --from-repofile=https://sing-box.app/sing-box.repo 2>/dev/null || dnf config-manager --add-repo https://sing-box.app/sing-box.repo
dnf install -y ` + pkgName(channel)
	return i.run(ctx, "sh", "-c", script)
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
