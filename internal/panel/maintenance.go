package panel

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Panel self-maintenance: report the running version, update the panel binary
// in place (binary/systemd installs only), and export a full data backup.
//
// The binary swap cannot happen inside this process: the systemd unit confines
// writes to the install dir (ProtectSystem=full makes /usr/local/bin read-only)
// and restarting the service would kill the very process doing the copy. Both
// problems are solved by handing the work to a transient `systemd-run` unit,
// which runs outside our sandbox and outlives our restart.

const (
	githubRepo      = "hann0w0/SingBox-Panel"
	serviceUnitName = "singbox-panel"
	serviceUnitPath = "/etc/systemd/system/singbox-panel.service"
	updateSubdir    = ".update" // under the install dir; holds the staged binary
)

// httpGetGitHub issues a GET with our User-Agent and returns the response on a
// 2xx; the caller closes Body. Centralizes the request boilerplate shared by
// the release lookup, binary download, and checksum fetch.
func httpGetGitHub(url string, timeout time.Duration) (*http.Response, error) {
	client := &http.Client{Timeout: timeout}
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "singbox-panel")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

// sqliteDBPath returns the configured SQLite file path and whether this is a
// file-backed SQLite deployment — the only kind backup/restore/self-update
// support. Callers phrase their own user-facing message on false.
func (a *App) sqliteDBPath() (string, bool) {
	if d := a.cfg.Database.Driver; d != "" && d != "sqlite" {
		return "", false
	}
	p := sqliteFilePath(a.cfg.Database.DSN)
	return p, p != ""
}

var (
	panelReleaseTag   string
	panelReleaseAt    time.Time
	panelReleaseMutex sync.Mutex
)

// latestPanelRelease returns the newest release tag from GitHub, cached for an
// hour so repeated dashboard visits do not hammer the anonymous API limit.
func latestPanelRelease() (string, error) {
	panelReleaseMutex.Lock()
	defer panelReleaseMutex.Unlock()
	if time.Since(panelReleaseAt) < time.Hour && panelReleaseTag != "" {
		return panelReleaseTag, nil
	}
	resp, err := httpGetGitHub("https://api.github.com/repos/"+githubRepo+"/releases/latest", 8*time.Second)
	if err != nil {
		return "", fmt.Errorf("访问 GitHub 失败：%w", err)
	}
	defer resp.Body.Close()
	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("解析 Release 失败：%w", err)
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("release 无 tag_name")
	}
	panelReleaseTag = rel.TagName
	panelReleaseAt = time.Now()
	return panelReleaseTag, nil
}

// selfUpdateSupported reports whether an in-place binary update is possible on
// this host. Docker deployments (no systemd unit) and non-systemd systems get a
// reason string the UI shows instead of the update button.
func selfUpdateSupported() (bool, string) {
	if runtime.GOOS != "linux" {
		return false, "仅 Linux 二进制部署支持面板自更新"
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false, "未检测到 systemd（可能是 Docker 部署，请用 install.sh 更新）"
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false, "未找到 systemctl"
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return false, "未找到 systemd-run"
	}
	if _, err := os.Stat(serviceUnitPath); err != nil {
		return false, "未检测到 systemd 服务单元（可能是 Docker 部署，请用 install.sh 更新）"
	}
	return true, ""
}

// GET /api/admin/maintenance/info — running version, latest release, and
// whether in-place self-update is available on this host.
func (a *App) maintenanceInfo(c *gin.Context) {
	supported, reason := selfUpdateSupported()
	driver := a.cfg.Database.Driver
	if driver == "" {
		driver = "sqlite"
	}
	resp := gin.H{
		"current_version":  a.version,
		"update_supported": supported,
		"db_driver":        driver,
		"uptime_seconds":   int64(time.Since(a.startedAt).Seconds()),
	}
	if !supported {
		resp["update_reason"] = reason
	}
	// The latest-release lookup is best-effort: a rate-limited or offline API
	// must not blank out the version card.
	if latest, err := latestPanelRelease(); err == nil {
		resp["latest_version"] = latest
		resp["has_update"] = supported && latest != "" && !sameVersion(latest, a.version)
	} else {
		resp["latest_error"] = err.Error()
	}
	c.JSON(http.StatusOK, resp)
}

// sameVersion compares tags tolerant of a leading v.
func sameVersion(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

// GET /api/admin/maintenance/backup — stream a .tar.gz containing a consistent
// SQLite snapshot plus the resolved jwt_secret. Restoring this on another host
// and pointing the same domain at it lets every agent reconnect automatically:
// agents authenticate with per-server tokens stored in the database, and user
// sessions survive because the jwt_secret is preserved.
func (a *App) downloadBackup(c *gin.Context) {
	// downloadBackup snapshots via VACUUM INTO rather than reading the live file
	// directly, so it only needs to confirm this is a SQLite deployment.
	if _, ok := a.sqliteDBPath(); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅 SQLite 部署支持一键备份"})
		return
	}

	// VACUUM INTO produces a valid snapshot even under WAL, without blocking
	// writers. Stage it in a private temp file we control, then stream it.
	snapDir, err := os.MkdirTemp("", "sbpanel-backup-")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建临时目录失败：" + err.Error()})
		return
	}
	defer os.RemoveAll(snapDir)
	snapPath := filepath.Join(snapDir, "singbox-panel.db")
	quoted := strings.ReplaceAll(snapPath, "'", "''")
	if err := a.db.Exec("VACUUM INTO '" + quoted + "'").Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成数据库快照失败：" + err.Error()})
		return
	}

	stamp := time.Now().Format("20060102-150405")
	c.Header("Content-Type", "application/gzip")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="singbox-panel-backup-%s.tar.gz"`, stamp))

	gz := gzip.NewWriter(c.Writer)
	tw := tar.NewWriter(gz)

	// The DB snapshot.
	if err := addFileToTar(tw, snapPath, "singbox-panel.db"); err != nil {
		// Headers are already sent; the truncated stream + logged error is the
		// best we can do to signal failure to the client.
		tw.Close()
		gz.Close()
		return
	}

	// The jwt_secret: from config if set, otherwise the persisted sidecar file.
	// Restoring this keeps existing admin/user sessions valid after migration.
	if secret, err := ResolveJWTSecret(a.cfg); err == nil && secret != "" {
		content := secret + "\n"
		hdr := &tar.Header{
			Name: "jwt_secret",
			Mode: 0o600,
			Size: int64(len(content)),
		}
		if tw.WriteHeader(hdr) == nil {
			_, _ = io.WriteString(tw, content)
		}
	}

	// A short manifest so a human opening the archive knows how to restore it.
	manifest := backupManifest(a.version, a.cfg.BaseURL)
	mh := &tar.Header{Name: "MANIFEST.txt", Mode: 0o644, Size: int64(len(manifest))}
	if tw.WriteHeader(mh) == nil {
		_, _ = io.WriteString(tw, manifest)
	}

	tw.Close()
	gz.Close()
}

func addFileToTar(tw *tar.Writer, path, nameInTar string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	hdr := &tar.Header{Name: nameInTar, Mode: 0o600, Size: info.Size()}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func backupManifest(version, baseURL string) string {
	return strings.Join([]string{
		"SingBox Panel 数据备份",
		"导出时间: " + time.Now().Format(time.RFC3339),
		"面板版本: " + version,
		"面板域名: " + baseURL,
		"",
		"内容:",
		"  singbox-panel.db  — 全部数据（节点、用户、订阅 token、被控 Agent 密钥）",
		"  jwt_secret        — 会话签名密钥（保留则迁移后登录不失效）",
		"",
		"迁移到新服务器:",
		"  1. 用 install.sh 以 binary 或 docker 方式装好面板",
		"  2. 停止面板服务",
		"  3. 用本备份里的 singbox-panel.db 覆盖新机数据目录下的同名文件",
		"  4. 把 jwt_secret 写入 panel.yaml 的 jwt_secret 字段（或数据目录的 .jwt_secret）",
		"  5. 启动面板；把原域名解析/反代指到新机",
		"  只要域名不变，被控 Agent 会用库里保存的 token 自动重连，无需重装。",
		"",
	}, "\n")
}

// POST /api/admin/maintenance/update — download the target release binary,
// verify its checksum, then hand a swap+restart script to a transient
// systemd-run unit so it survives our own restart and escapes the sandbox.
func (a *App) selfUpdate(c *gin.Context) {
	if ok := a.selfUpdating.TryLock(); !ok {
		c.JSON(http.StatusConflict, gin.H{"error": "已有更新任务正在进行"})
		return
	}
	defer a.selfUpdating.Unlock()

	supported, reason := selfUpdateSupported()
	if !supported {
		c.JSON(http.StatusBadRequest, gin.H{"error": reason})
		return
	}

	var body struct {
		Version string `json:"version"`
	}
	if !bindOptionalJSON(c, &body) {
		return
	}
	target := strings.TrimSpace(body.Version)
	if target == "" {
		latest, err := latestPanelRelease()
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		target = latest
	}
	// Reject anything that is not a plain release tag: the value is interpolated
	// into a download URL and a shell script.
	if !validReleaseTag(target) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法的版本号：" + target})
		return
	}
	if sameVersion(target, a.version) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "message": "已是目标版本 " + target + "，无需更新", "updated": false})
		return
	}

	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的架构：" + arch})
		return
	}
	assetName := "singbox-panel-linux-" + arch
	base := "https://github.com/" + githubRepo + "/releases/download/" + target

	// Stage the new binary + checksum inside the install dir, which is the one
	// path the sandbox lets us write. systemd-run will pick it up from here.
	stageDir := filepath.Join(installDirFromDSN(a.cfg.Database.DSN), updateSubdir)
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建暂存目录失败：" + err.Error()})
		return
	}
	stagedBin := filepath.Join(stageDir, assetName)

	if err := downloadFile(base+"/"+assetName, stagedBin); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "下载二进制失败：" + err.Error()})
		return
	}
	// Verify against checksums.txt; a missing/mismatched sum aborts the swap.
	if err := verifyReleaseChecksum(base+"/checksums.txt", stagedBin, assetName); err != nil {
		_ = os.Remove(stagedBin)
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if err := os.Chmod(stagedBin, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "设置可执行权限失败：" + err.Error()})
		return
	}

	// Detach the swap to a transient unit: it copies the staged binary over the
	// live one and restarts the service. Doing this in-process is impossible —
	// the restart would kill us mid-copy and the sandbox blocks the write.
	if err := launchSwap(stagedBin); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "启动更新任务失败：" + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"updated": true,
		"message": "已开始更新到 " + target + "，面板将在数秒后重启。",
		"version": target,
	})
}

// validReleaseTag allows only vX.Y.Z-style tags: digits, dots, dashes and a
// leading v. This value reaches both a URL and a shell command line.
func validReleaseTag(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// installDirFromDSN derives the install directory from the SQLite DSN. The DB
// lives at <install>/data/singbox-panel.db, so the install dir is two levels up.
func installDirFromDSN(dsn string) string {
	dbPath := sqliteFilePath(dsn)
	if dbPath == "" {
		return "/opt/singbox-panel"
	}
	return filepath.Dir(filepath.Dir(dbPath)) // strip /data/singbox-panel.db
}

func downloadFile(url, dest string) error {
	resp, err := httpGetGitHub(url, 5*time.Minute)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// verifyReleaseChecksum downloads checksums.txt and confirms the file's SHA256
// matches the entry for assetName. A missing entry or mismatch is fatal.
func verifyReleaseChecksum(checksumsURL, filePath, assetName string) error {
	resp, err := httpGetGitHub(checksumsURL, 30*time.Second)
	if err != nil {
		return fmt.Errorf("下载 checksums.txt 失败：%w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("读取 checksums.txt 失败：%w", err)
	}
	want := ""
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*") // BSD-style "*name"
		if name == assetName {
			want = strings.ToLower(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("checksums.txt 未包含 %s，拒绝安装未校验产物", assetName)
	}
	got, err := sha256File(filePath)
	if err != nil {
		return fmt.Errorf("计算 SHA256 失败：%w", err)
	}
	if got != want {
		return fmt.Errorf("SHA256 校验失败：期望 %s 实际 %s", want, got)
	}
	return nil
}

func sha256File(path string) (string, error) {
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

// launchSwap runs the binary swap + service restart in a transient systemd unit
// so it escapes this service's sandbox (ProtectSystem=full) and survives the
// restart that kills this process. The script waits briefly for our HTTP
// response to flush, installs the staged binary, and restarts the unit.
func launchSwap(stagedBin string) error {
	script := fmt.Sprintf(`sleep 2
install -m 0755 %[1]s %[2]s || exit 1
rm -f %[1]s
systemctl restart %[3]s`,
		shellQuote(stagedBin), shellQuote(panelBinaryPath()), shellQuote(serviceUnitName))

	cmd := exec.Command("systemd-run",
		"--collect",
		"--unit", fmt.Sprintf("singbox-panel-selfupdate-%d", time.Now().Unix()),
		"/bin/sh", "-c", script,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// panelBinaryPath returns the path to the running panel binary so the swap
// overwrites the correct file even for a non-standard install.
func panelBinaryPath() string {
	if p, err := os.Executable(); err == nil && p != "" {
		return p
	}
	return "/usr/local/bin/singbox-panel"
}
