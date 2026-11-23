package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

const maxSingboxInstallScriptBytes = 1 << 20 // 1 MiB

// Official sing-box paths and service name.
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
// (script | apt | dnf) and channel (stable | beta). It only ever invokes
// the official installer/repositories documented by upstream (or official
// GitHub musl releases on Alpine Linux where upstream .apk assets require
// apk-tools v3).
func InstallSingbox(ctx context.Context, channel, version, method string) (string, error) {
	installer := singboxInstaller{
		run:            run,
		detectVersion:  DetectVersion,
		prepareService: prepareSingboxService,
		isAlpine:       isAlpineHost,
		installAlpine:  installAlpineRelease,
		serviceActive:  ServiceActive,
		restartService: func(ctx context.Context) error {
			_, err := ServiceAction(ctx, protocol.SvcRestart)
			return err
		},
	}
	return installer.install(ctx, channel, version, method)
}

type singboxInstaller struct {
	run            commandRunner
	detectVersion  func(context.Context) (bool, string)
	prepareService func(context.Context) error
	isAlpine       func() bool
	installAlpine  func(context.Context, string, string) (string, error)
	serviceActive  func(context.Context) bool
	restartService func(context.Context) error
}

func (i singboxInstaller) install(ctx context.Context, channel, version, method string) (string, error) {
	if channel == "" {
		channel = protocol.ChannelStable
	}
	if channel != protocol.ChannelStable && channel != protocol.ChannelBeta {
		return "", fmt.Errorf("unknown sing-box channel %q", channel)
	}
	if version != "" && !versionRe.MatchString(version) {
		return "", fmt.Errorf("invalid version %q", version)
	}
	if version != "" && (method == protocol.MethodAPT || method == protocol.MethodDNF) {
		return "", fmt.Errorf("specific sing-box versions require the script installation method")
	}
	alpine := i.isAlpine != nil && i.isAlpine()
	wasActive := false
	if alpine && i.serviceActive != nil {
		wasActive = i.serviceActive(ctx)
	}
	var output string
	var err error
	switch method {
	case protocol.MethodAPT:
		output, err = i.installAPT(ctx, channel)
	case protocol.MethodDNF:
		output, err = i.installDNF(ctx, channel)
	case protocol.MethodScript, "":
		if alpine && i.installAlpine != nil {
			output, err = i.installAlpine(ctx, channel, version)
		} else {
			output, err = i.installScript(ctx, channel, version)
		}
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
	if i.prepareService != nil {
		if err := i.prepareService(ctx); err != nil {
			return output, fmt.Errorf("prepare sing-box service: %w", err)
		}
	}
	if wasActive && i.restartService != nil {
		if err := i.restartService(ctx); err != nil {
			return output, fmt.Errorf("restart sing-box service after upgrade: %w", err)
		}
	}
	return output, nil
}

func prepareSingboxService(ctx context.Context) error {
	manager, err := currentServiceManager()
	if err != nil {
		return err
	}
	return prepareSingboxServiceWithManager(ctx, manager)
}

func prepareSingboxServiceWithManager(ctx context.Context, manager serviceManager) error {
	if out, err := manager.Prepare(ctx); err != nil {
		return fmt.Errorf("prepare service: %w: %s", err, out)
	}
	if !manager.Enabled(ctx) {
		if out, err := manager.SetEnabled(ctx, true); err != nil {
			return fmt.Errorf("enable service at boot: %w: %s", err, out)
		}
	}
	return nil
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
	output, err := i.run(ctx, "curl", "-fsSL", "--output", path,
		"--proto", "=https",
		"--proto-redir", "=https",
		"--max-redirs", "5",
		"--max-filesize", fmt.Sprintf("%d", maxSingboxInstallScriptBytes),
		"--connect-timeout", "15",
		"--max-time", "60",
		"https://sing-box.app/install.sh")
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
	if info.Size() > maxSingboxInstallScriptBytes {
		return output, fmt.Errorf("downloaded sing-box installer exceeds %d bytes", maxSingboxInstallScriptBytes)
	}
	scriptBytes, err := os.ReadFile(path)
	if err != nil {
		return output, fmt.Errorf("read sing-box installer: %w", err)
	}
	if bytes.IndexByte(scriptBytes, 0) >= 0 || !utf8.Valid(scriptBytes) {
		return output, fmt.Errorf("downloaded sing-box installer is not a valid shell script")
	}
	if pinned := strings.TrimSpace(os.Getenv("SINGBOX_INSTALL_SCRIPT_SHA256")); pinned != "" {
		sum := sha256.Sum256(scriptBytes)
		actual := hex.EncodeToString(sum[:])
		if !strings.EqualFold(actual, pinned) {
			return output, fmt.Errorf("sing-box installer SHA256 mismatch: got %s, want %s", actual, strings.ToLower(pinned))
		}
	}
	_ = os.Chmod(path, 0o600)
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

var (
	versionCacheMu      sync.Mutex
	versionCachePath    string
	versionCacheSize    int64
	versionCacheModTime time.Time
	versionCacheValue   string
)

// DetectVersion reports whether the sing-box binary is present and its version.
func DetectVersion(ctx context.Context) (installed bool, version string) {
	bin := singboxBinary()
	info, err := os.Stat(bin)
	if err != nil {
		versionCacheMu.Lock()
		versionCachePath = ""
		versionCacheValue = ""
		versionCacheMu.Unlock()
		return false, ""
	}
	versionCacheMu.Lock()
	if versionCachePath == bin && versionCacheSize == info.Size() && versionCacheModTime.Equal(info.ModTime()) && versionCacheValue != "" {
		cached := versionCacheValue
		versionCacheMu.Unlock()
		return true, cached
	}
	versionCacheMu.Unlock()

	var detected string
	if out, err := run(ctx, bin, "version", "-n"); err == nil && out != "" {
		detected = firstLine(out)
	} else if out, err := run(ctx, bin, "version"); err == nil {
		// Fallback for builds without `-n`: parse `sing-box version <X> ...`.
		fields := strings.Fields(firstLine(out))
		if len(fields) >= 3 && fields[0] == "sing-box" && fields[1] == "version" {
			detected = fields[2]
		} else {
			detected = firstLine(out)
		}
	}
	if detected != "" {
		versionCacheMu.Lock()
		versionCachePath = bin
		versionCacheSize = info.Size()
		versionCacheModTime = info.ModTime()
		versionCacheValue = detected
		versionCacheMu.Unlock()
	}
	return true, detected
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
