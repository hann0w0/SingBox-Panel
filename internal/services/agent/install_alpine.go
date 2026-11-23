package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

const (
	singboxStateDir                   = "/var/lib/sing-box"
	defaultGitHubAPIBaseURL           = "https://api.github.com"
	defaultGitHubDownloadBase         = "https://github.com"
	maxSingboxBinaryBytes             = 256 << 20
	maxAlpineArchiveCompressedBytes   = 256 << 20
	maxAlpineArchiveDecompressedBytes = 512 << 20
	maxAlpineArchiveEntries           = 1024
	maxAlpineBackupBytes              = 256 << 20
	maxAlpineBackupFiles              = 4096
	maxGitHubReleasePayloadSize       = 4 << 20
	alpineReleaseQueryTimeout         = 30 * time.Second
	alpineArchiveDownloadTimeout      = 3 * time.Minute
)

var errReleaseAssetNotFound = errors.New("release asset not found")

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

type alpineReleaseCandidate struct {
	url            string
	expectedSHA256 string
}

type githubReleasePayload struct {
	TagName string               `json:"tag_name"`
	Assets  []githubReleaseAsset `json:"assets"`
}

type alpineReleaseInstaller struct {
	httpClient      *http.Client
	apiBaseURL      string
	downloadBaseURL string
	goarch          string
	targetBinary    string
	configDir       string
	stateDir        string
	githubToken     string
	run             commandRunner
	hasAPK          func() bool
	hasRCService    func() bool
}

func isAlpineHost() bool {
	if pathExists("/etc/alpine-release") {
		return true
	}
	if !commandExists("apk") {
		return false
	}
	if raw, err := os.ReadFile("/etc/os-release"); err == nil && strings.Contains(string(raw), "OPENWRT_ARCH") {
		return false
	}
	return !commandExists("dpkg") && !commandExists("rpm") && !commandExists("pacman")
}

func defaultAlpineHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = 15 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: transport}
}

func installAlpineRelease(ctx context.Context, channel, version string) (string, error) {
	installer := alpineReleaseInstaller{
		httpClient:      defaultAlpineHTTPClient(),
		apiBaseURL:      defaultGitHubAPIBaseURL,
		downloadBaseURL: defaultGitHubDownloadBase,
		goarch:          runtime.GOARCH,
		targetBinary:    SingboxBinary,
		configDir:       ConfigDir,
		stateDir:        singboxStateDir,
		githubToken:     strings.TrimSpace(os.Getenv("GITHUB_TOKEN")),
		run:             run,
		hasAPK:          func() bool { return commandExists("apk") },
		hasRCService:    func() bool { return commandExists("rc-service") },
	}
	return installer.install(ctx, channel, version)
}

func alpineReleaseArch(goarch string) (string, error) {
	switch goarch {
	case "amd64", "arm64", "386", "loong64", "riscv64":
		return goarch, nil
	case "arm":
		return "armv7", nil
	default:
		return "", fmt.Errorf("unsupported Alpine architecture %q for sing-box release", goarch)
	}
}

func (a alpineReleaseInstaller) client() *http.Client {
	if a.httpClient != nil {
		return a.httpClient
	}
	return defaultAlpineHTTPClient()
}

func (a alpineReleaseInstaller) install(ctx context.Context, channel, version string) (string, error) {
	arch, err := alpineReleaseArch(a.goarch)
	if err != nil {
		return "", err
	}
	var logLines []string
	var candidates []alpineReleaseCandidate
	targetVersion := version
	if targetVersion == "" {
		queryCtx, cancel := context.WithTimeout(ctx, alpineReleaseQueryTimeout)
		rel, err := a.resolveLatestRelease(queryCtx, channel)
		cancel()
		if err != nil {
			return "", err
		}
		targetVersion = strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
		if targetVersion == "" || !versionRe.MatchString(targetVersion) {
			return "", fmt.Errorf("unexpected sing-box release version %q", rel.TagName)
		}
		candidates = selectAlpineAssetCandidates(rel.Assets, targetVersion, arch)
	}
	if len(candidates) == 0 {
		for _, u := range alpineReleaseDownloadURLs(a.downloadBaseURL, targetVersion, arch) {
			candidates = append(candidates, alpineReleaseCandidate{url: u})
		}
	}

	tmpDir, err := os.MkdirTemp("", "singbox-alpine-install-*")
	if err != nil {
		return "", fmt.Errorf("create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	stagedBin := filepath.Join(tmpDir, "sing-box")
	var downloadedURL string
	var downloadErr error
	for _, candidate := range candidates {
		logLines = append(logLines, "Downloading "+candidate.url)
		dlCtx, cancel := context.WithTimeout(ctx, alpineArchiveDownloadTimeout)
		downloadErr = a.downloadAndExtractBinary(dlCtx, candidate.url, candidate.expectedSHA256, stagedBin)
		cancel()
		if downloadErr == nil {
			downloadedURL = candidate.url
			break
		}
		if errors.Is(downloadErr, errReleaseAssetNotFound) {
			continue
		}
		return strings.Join(logLines, "\n"), downloadErr
	}
	if downloadedURL == "" {
		if downloadErr == nil {
			downloadErr = errReleaseAssetNotFound
		}
		return strings.Join(logLines, "\n"), fmt.Errorf("download sing-box %s (%s): %w", targetVersion, arch, downloadErr)
	}

	verifyOut, err := a.run(ctx, stagedBin, "version")
	if verifyOut != "" {
		logLines = append(logLines, verifyOut)
	}
	if err != nil {
		return strings.Join(logLines, "\n"), fmt.Errorf("verify downloaded sing-box binary on Alpine: %w", err)
	}

	if a.hasAPK != nil && a.hasAPK() {
		apkOut, err := a.removeInstalledAPKPackages(ctx, tmpDir)
		if apkOut != "" {
			logLines = append(logLines, apkOut)
		}
		if err != nil {
			return strings.Join(logLines, "\n"), err
		}
	}

	if err := os.MkdirAll(filepath.Dir(a.targetBinary), 0o755); err != nil {
		return strings.Join(logLines, "\n"), fmt.Errorf("create binary directory: %w", err)
	}
	if err := os.MkdirAll(a.configDir, 0o755); err != nil {
		return strings.Join(logLines, "\n"), fmt.Errorf("create config directory: %w", err)
	}
	if err := os.MkdirAll(a.stateDir, 0o755); err != nil {
		return strings.Join(logLines, "\n"), fmt.Errorf("create state directory: %w", err)
	}
	if err := installExecutableAtomically(stagedBin, a.targetBinary); err != nil {
		return strings.Join(logLines, "\n"), fmt.Errorf("install sing-box binary: %w", err)
	}
	logLines = append(logLines, fmt.Sprintf("Installed sing-box %s to %s", targetVersion, a.targetBinary))
	return strings.Join(logLines, "\n"), nil
}

func (a alpineReleaseInstaller) newRequest(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "singbox-panel-agent")
	if a.githubToken != "" {
		req.Header.Set("Authorization", "token "+a.githubToken)
	}
	return req, nil
}

func (a alpineReleaseInstaller) resolveLatestRelease(ctx context.Context, channel string) (githubReleasePayload, error) {
	base := strings.TrimRight(a.apiBaseURL, "/")
	endpoint := base + "/repos/SagerNet/sing-box/releases/latest"
	if channel == protocol.ChannelBeta {
		endpoint = base + "/repos/SagerNet/sing-box/releases?per_page=15"
	}
	req, err := a.newRequest(ctx, endpoint)
	if err != nil {
		return githubReleasePayload{}, fmt.Errorf("build release query: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := a.client().Do(req)
	if err != nil {
		return githubReleasePayload{}, fmt.Errorf("query sing-box release: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGitHubReleasePayloadSize))
	if err != nil {
		return githubReleasePayload{}, fmt.Errorf("read sing-box release response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return githubReleasePayload{}, fmt.Errorf("query sing-box release: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if channel == protocol.ChannelBeta {
		var list []githubReleasePayload
		if err := json.Unmarshal(body, &list); err != nil {
			return githubReleasePayload{}, fmt.Errorf("parse sing-box releases: %w", err)
		}
		for _, item := range list {
			if strings.TrimSpace(item.TagName) != "" {
				return item, nil
			}
		}
		return githubReleasePayload{}, errors.New("no sing-box releases found")
	}
	var latest githubReleasePayload
	if err := json.Unmarshal(body, &latest); err != nil {
		return githubReleasePayload{}, fmt.Errorf("parse sing-box release: %w", err)
	}
	if strings.TrimSpace(latest.TagName) == "" {
		return githubReleasePayload{}, errors.New("latest sing-box release tag is empty")
	}
	return latest, nil
}

func alpineReleaseAssetNames(version, arch string) []string {
	return []string{
		fmt.Sprintf("sing-box-%s-linux-%s-musl.tar.gz", version, arch),
		fmt.Sprintf("sing-box-%s-linux-%s.tar.gz", version, arch),
	}
}

func parseSHA256Digest(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if !strings.HasPrefix(raw, "sha256:") {
		return ""
	}
	hexPart := strings.TrimPrefix(raw, "sha256:")
	if b, err := hex.DecodeString(hexPart); err != nil || len(b) != sha256.Size {
		return ""
	}
	return hexPart
}

func selectAlpineAssetCandidates(assets []githubReleaseAsset, version, arch string) []alpineReleaseCandidate {
	byName := make(map[string]githubReleaseAsset, len(assets))
	for _, asset := range assets {
		if asset.Name != "" && asset.BrowserDownloadURL != "" {
			byName[asset.Name] = asset
		}
	}
	var out []alpineReleaseCandidate
	for _, name := range alpineReleaseAssetNames(version, arch) {
		if asset, ok := byName[name]; ok {
			out = append(out, alpineReleaseCandidate{
				url:            asset.BrowserDownloadURL,
				expectedSHA256: parseSHA256Digest(asset.Digest),
			})
		}
	}
	return out
}

func selectAlpineAssetURLs(assets []githubReleaseAsset, version, arch string) []string {
	candidates := selectAlpineAssetCandidates(assets, version, arch)
	urls := make([]string, 0, len(candidates))
	for _, c := range candidates {
		urls = append(urls, c.url)
	}
	return urls
}

func alpineReleaseDownloadURLs(downloadBase, version, arch string) []string {
	base := strings.TrimRight(downloadBase, "/")
	names := alpineReleaseAssetNames(version, arch)
	urls := make([]string, 0, len(names))
	for _, name := range names {
		urls = append(urls, fmt.Sprintf("%s/SagerNet/sing-box/releases/download/v%s/%s", base, version, name))
	}
	return urls
}

type strictLimitReader struct {
	r     io.Reader
	limit int64
	read  int64
	err   error
}

func (l *strictLimitReader) Read(p []byte) (int, error) {
	if l.read >= l.limit {
		return 0, l.err
	}
	remaining := l.limit - l.read
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := l.r.Read(p)
	l.read += int64(n)
	if err == nil && l.read >= l.limit {
		// Peek one extra byte to detect overflow past exact limit.
		var extra [1]byte
		en, eerr := l.r.Read(extra[:])
		if en > 0 {
			return n, l.err
		}
		if eerr != nil && !errors.Is(eerr, io.EOF) {
			return n, eerr
		}
	}
	return n, err
}

func (a alpineReleaseInstaller) downloadAndExtractBinary(ctx context.Context, url, expectedSHA256, dstPath string) error {
	req, err := a.newRequest(ctx, url)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return fmt.Errorf("download sing-box archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errReleaseAssetNotFound
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("download sing-box archive: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	bounded := &strictLimitReader{
		r:     resp.Body,
		limit: maxAlpineArchiveCompressedBytes,
		err:   fmt.Errorf("sing-box archive exceeds %d bytes", maxAlpineArchiveCompressedBytes),
	}
	if expectedSHA256 != "" {
		h := sha256.New()
		tee := io.TeeReader(bounded, h)
		if err := extractSingboxFromTarGz(tee, dstPath); err != nil {
			_ = os.Remove(dstPath)
			return err
		}
		if _, err := io.Copy(io.Discard, tee); err != nil {
			_ = os.Remove(dstPath)
			return fmt.Errorf("read remaining sing-box archive for checksum: %w", err)
		}
		actual := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(actual, expectedSHA256) {
			_ = os.Remove(dstPath)
			return fmt.Errorf("sing-box archive SHA256 mismatch: got %s, want %s", actual, expectedSHA256)
		}
		return nil
	}
	return extractSingboxFromTarGz(bounded, dstPath)
}

func extractSingboxFromTarGz(r io.Reader, dstPath string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open sing-box gzip archive: %w", err)
	}
	defer gz.Close()
	decompressed := &strictLimitReader{
		r:     gz,
		limit: maxAlpineArchiveDecompressedBytes,
		err:   fmt.Errorf("sing-box decompressed archive exceeds %d bytes", maxAlpineArchiveDecompressedBytes),
	}
	tr := tar.NewReader(decompressed)
	entries := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read sing-box tar archive: %w", err)
		}
		entries++
		if entries > maxAlpineArchiveEntries {
			return fmt.Errorf("sing-box tar archive exceeds %d entries", maxAlpineArchiveEntries)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "sing-box" {
			continue
		}
		out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return fmt.Errorf("create staged sing-box binary: %w", err)
		}
		n, copyErr := io.Copy(out, io.LimitReader(tr, maxSingboxBinaryBytes+1))
		closeErr := out.Close()
		if copyErr != nil {
			_ = os.Remove(dstPath)
			return fmt.Errorf("extract sing-box binary: %w", copyErr)
		}
		if closeErr != nil {
			_ = os.Remove(dstPath)
			return fmt.Errorf("close staged sing-box binary: %w", closeErr)
		}
		if n == 0 {
			_ = os.Remove(dstPath)
			return errors.New("extracted sing-box binary is empty")
		}
		if n > maxSingboxBinaryBytes {
			_ = os.Remove(dstPath)
			return fmt.Errorf("extracted sing-box binary exceeds %d bytes", maxSingboxBinaryBytes)
		}
		return nil
	}
	return errors.New("sing-box binary not found in release archive")
}

// removeInstalledAPKPackages removes distro/third-party apk sing-box packages
// before installing the official musl release binary so future `apk upgrade`
// runs cannot overwrite /usr/bin/sing-box with an older repository package.
// Preserved configuration and state directories are snapshotted and restored.
func (a alpineReleaseInstaller) removeInstalledAPKPackages(ctx context.Context, tmpDir string) (string, error) {
	packages := []string{"sing-box-openrc", "sing-box-beta", "sing-box"}
	var installed []string
	for _, pkg := range packages {
		if _, err := a.run(ctx, "apk", "info", "-e", pkg); err == nil {
			installed = append(installed, pkg)
		}
	}
	if len(installed) == 0 {
		return "", nil
	}

	var outputs []string
	configBackup := filepath.Join(tmpDir, "config-backup")
	stateBackup := filepath.Join(tmpDir, "state-backup")
	hadConfig, err := snapshotDirectoryIfExists(a.configDir, configBackup)
	if err != nil {
		return "", fmt.Errorf("backup %s before removing apk package: %w", a.configDir, err)
	}
	hadState, err := snapshotDirectoryIfExists(a.stateDir, stateBackup)
	if err != nil {
		return "", fmt.Errorf("backup %s before removing apk package: %w", a.stateDir, err)
	}

	if a.hasRCService != nil && a.hasRCService() {
		_, _ = a.run(ctx, "rc-service", ServiceName, "stop")
	}

	var delErr error
	for _, pkg := range installed {
		if _, err := a.run(ctx, "apk", "info", "-e", pkg); err != nil {
			continue
		}
		out, err := a.run(ctx, "apk", "del", pkg)
		if out != "" {
			outputs = append(outputs, out)
		}
		if err != nil {
			delErr = fmt.Errorf("remove existing apk package %s: %w", pkg, err)
			break
		}
	}

	if hadConfig {
		if err := restoreDirectoryMissing(configBackup, a.configDir); err != nil && delErr == nil {
			delErr = fmt.Errorf("restore %s after removing apk package: %w", a.configDir, err)
		}
	}
	if hadState {
		if err := restoreDirectoryMissing(stateBackup, a.stateDir); err != nil && delErr == nil {
			delErr = fmt.Errorf("restore %s after removing apk package: %w", a.stateDir, err)
		}
	}
	return strings.Join(outputs, "\n"), delErr
}

func snapshotDirectoryIfExists(srcDir, dstDir string) (bool, error) {
	info, err := os.Stat(srcDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}
	if err := copyDirTree(srcDir, dstDir, false); err != nil {
		return false, err
	}
	return true, nil
}

func restoreDirectoryMissing(backupDir, targetDir string) error {
	return copyDirTree(backupDir, targetDir, true)
}

func copyDirTree(srcDir, dstDir string, skipExisting bool) error {
	cleanDstRoot := filepath.Clean(dstDir)
	var totalBytes int64
	var fileCount int
	return filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		dst := dstDir
		if rel != "." {
			dst = filepath.Join(dstDir, rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(dst, info.Mode().Perm())
		}
		fileCount++
		if fileCount > maxAlpineBackupFiles {
			return fmt.Errorf("backup directory exceeds %d files", maxAlpineBackupFiles)
		}
		if d.Type()&os.ModeSymlink != 0 {
			if skipExisting {
				if _, lstatErr := os.Lstat(dst); lstatErr == nil {
					return nil
				}
			}
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			resolved := linkTarget
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(filepath.Dir(dst), linkTarget)
			}
			resolved = filepath.Clean(resolved)
			if relTarget, err := filepath.Rel(cleanDstRoot, resolved); err != nil || relTarget == ".." || strings.HasPrefix(relTarget, ".."+string(os.PathSeparator)) {
				return fmt.Errorf("refusing symlink %q pointing outside %q", rel, cleanDstRoot)
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			_ = os.Remove(dst)
			return os.Symlink(linkTarget, dst)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if skipExisting {
			if _, statErr := os.Stat(dst); statErr == nil {
				return nil
			}
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		written, err := copyRegularFileBounded(path, dst, info.Mode().Perm(), maxAlpineBackupBytes-totalBytes)
		totalBytes += written
		return err
	})
}

func copyRegularFileBounded(src, dst string, mode os.FileMode, remaining int64) (int64, error) {
	if remaining < 0 {
		return 0, fmt.Errorf("backup exceeds %d-byte limit", maxAlpineBackupBytes)
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(in, remaining+1))
	if err != nil {
		out.Close()
		_ = os.Remove(dst)
		return n, err
	}
	if n > remaining {
		out.Close()
		_ = os.Remove(dst)
		return n, fmt.Errorf("backup exceeds %d-byte limit", maxAlpineBackupBytes)
	}
	return n, out.Close()
}

func installExecutableAtomically(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".sing-box.install-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return err
	}
	cleanup = false
	return nil
}
