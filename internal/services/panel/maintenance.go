package panel

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
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	updateSubdir    = ".update" // under the install dir; holds one transactional release bundle
	maxUpdateAsset  = 256 << 20
)

// httpGetGitHub issues a GET with our User-Agent and returns the response on a
// 2xx; the caller closes Body. Centralizes the request boilerplate shared by
// the release lookup, binary download, and checksum fetch.
func httpGetGitHub(url string, timeout time.Duration) (*http.Response, error) {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
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
	panelReleaseURL   = "https://api.github.com/repos/" + githubRepo + "/releases/latest"
)

func (a *App) tryMaintenanceRequest(c *gin.Context) (func(), bool) {
	if !a.selfUpdating.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "已有维护任务正在进行"})
		return func() {}, false
	}
	return a.selfUpdating.Unlock, true
}

// latestPanelRelease returns the newest release tag from GitHub. Automatic
// page loads reuse the one-hour cache; an explicit refresh bypasses it so a
// release published after the panel started is visible without a restart.
func latestPanelRelease(refresh bool) (string, error) {
	panelReleaseMutex.Lock()
	defer panelReleaseMutex.Unlock()
	if !refresh && time.Since(panelReleaseAt) < time.Hour && panelReleaseTag != "" {
		return panelReleaseTag, nil
	}
	resp, err := httpGetGitHub(panelReleaseURL, 8*time.Second)
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
// this host. Non-systemd systems get a reason string the UI shows instead of
// the update button.
func selfUpdateSupported() (bool, string) {
	if runtime.GOOS != "linux" {
		return false, "仅 Linux 二进制部署支持面板自更新"
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false, "未检测到 systemd，无法执行面板自更新"
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false, "未找到 systemctl"
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return false, "未找到 systemd-run"
	}
	if _, err := os.Stat(serviceUnitPath); err != nil {
		return false, "未检测到 singbox-panel systemd 服务单元，请用 install.sh 修复安装"
	}
	if os.Geteuid() != 0 {
		return false, "面板已使用低权限账户运行；请重新执行 install.sh 完成事务化更新"
	}
	return true, ""
}

// GET /api/admin/maintenance/info — running version, latest release, and
// whether in-place self-update is available on this host.
func (a *App) maintenanceInfo(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
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
	refresh := c.Query("refresh") == "1" || strings.EqualFold(c.Query("refresh"), "true")
	if latest, err := latestPanelRelease(refresh); err == nil {
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
	if _, ok := a.sqliteDBPath(); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅 SQLite 部署支持一键备份"})
		return
	}
	if !a.selfUpdating.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "已有维护任务正在进行"})
		return
	}
	maintenanceUnlocked := false
	defer func() {
		if !maintenanceUnlocked {
			a.selfUpdating.Unlock()
		}
	}()
	archivePath, archiveName, cleanup, err := a.createBackupArchive()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// The archive is now an immutable private snapshot. Release the maintenance
	// gate before streaming it to a slow client; a later restore/update cannot
	// alter this already-created file.
	a.selfUpdating.Unlock()
	maintenanceUnlocked = true
	defer cleanup()

	c.Header("Content-Type", "application/gzip")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, archiveName))
	f, err := os.Open(archivePath)
	if err != nil {
		c.Error(err)
		return
	}
	defer f.Close()
	_, _ = io.Copy(c.Writer, f)
}

// createBackupArchive creates the same data-only archive exposed by the
// download endpoint. Keeping archive creation separate lets the OneDrive
// integration upload exactly this file without including binaries or runtime
// directories.
func (a *App) createBackupArchive() (archivePath, archiveName string, cleanup func(), err error) {
	cleanup = func() {}
	dbPath, ok := a.sqliteDBPath()
	if !ok {
		return "", "", cleanup, fmt.Errorf("仅 SQLite 部署支持一键备份")
	}

	// Keep the snapshot beside the database instead of /tmp. Hardened services
	// may cap /tmp, while the data directory is the storage whose free
	// space actually determines whether a database backup can succeed.
	snapDir, err := os.MkdirTemp(filepath.Dir(dbPath), ".sbpanel-backup-")
	if err != nil {
		return "", "", cleanup, fmt.Errorf("创建临时目录失败：%w", err)
	}
	cleanup = func() { _ = os.RemoveAll(snapDir) }
	snapPath := filepath.Join(snapDir, "singbox-panel.db")
	quoted := strings.ReplaceAll(snapPath, "'", "''")
	if err := a.db.Exec("VACUUM INTO '" + quoted + "'").Error; err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("生成数据库快照失败：%w", err)
	}

	archiveName = fmt.Sprintf("singbox-panel-backup-%s.tar.gz", time.Now().Format("20060102-150405"))
	archivePath = filepath.Join(snapDir, archiveName)
	f, err := os.Create(archivePath)
	if err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("创建备份归档失败：%w", err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	closeArchive := func() error {
		if err := tw.Close(); err != nil {
			_ = f.Close()
			return err
		}
		if err := gz.Close(); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	}

	if err := addFileToTar(tw, snapPath, "singbox-panel.db"); err != nil {
		_ = closeArchive()
		cleanup()
		return "", "", func() {}, fmt.Errorf("写入数据库快照失败：%w", err)
	}

	secret, err := ResolveJWTSecret(a.cfg)
	if err != nil || isWeakSecret(secret) {
		_ = closeArchive()
		cleanup()
		if err != nil {
			return "", "", func() {}, fmt.Errorf("读取会话密钥失败：%w", err)
		}
		return "", "", func() {}, fmt.Errorf("会话密钥不符合安全要求，拒绝生成不完整备份")
	}
	content := secret + "\n"
	hdr := &tar.Header{Name: "jwt_secret", Mode: 0o600, Size: int64(len(content))}
	if err := tw.WriteHeader(hdr); err != nil {
		_ = closeArchive()
		cleanup()
		return "", "", func() {}, fmt.Errorf("写入会话密钥失败：%w", err)
	}
	if _, err := io.WriteString(tw, content); err != nil {
		_ = closeArchive()
		cleanup()
		return "", "", func() {}, fmt.Errorf("写入会话密钥失败：%w", err)
	}

	databaseSHA256, err := hashFile(snapPath)
	if err != nil {
		_ = closeArchive()
		cleanup()
		return "", "", func() {}, fmt.Errorf("计算数据库校验值失败：%w", err)
	}
	snapshotDB, err := openSQLiteReadOnly(snapPath)
	if err != nil {
		_ = closeArchive()
		cleanup()
		return "", "", func() {}, fmt.Errorf("读取数据库快照失败：%w", err)
	}
	credentials, credentialErr := calculateCredentialFingerprint(snapshotDB)
	schemaVersion, schemaErr := schemaVersionInDB(snapshotDB)
	if sqlDB, closeErr := snapshotDB.DB(); closeErr == nil {
		_ = sqlDB.Close()
	}
	if credentialErr != nil {
		_ = closeArchive()
		cleanup()
		return "", "", func() {}, fmt.Errorf("计算节点凭据校验值失败：%w", credentialErr)
	}
	if !credentials.Available {
		_ = closeArchive()
		cleanup()
		return "", "", func() {}, fmt.Errorf("数据库中存在缺少稳定种子的用户，无法生成安全备份")
	}
	if schemaErr != nil {
		_ = closeArchive()
		cleanup()
		return "", "", func() {}, fmt.Errorf("读取数据库版本失败：%w", schemaErr)
	}
	if schemaVersion == 0 {
		// A normally-started panel always has the migration ledger. This fallback
		// only covers manually-created compatible databases and test fixtures.
		schemaVersion = currentSchemaVersion()
	}
	metadataRaw, err := json.MarshalIndent(backupMetadata{
		FormatVersion: backupFormatVersion, PanelVersion: a.version,
		SchemaVersion: schemaVersion, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		DatabaseDriver: "sqlite", DatabaseSHA256: databaseSHA256, BaseURL: a.cfg.BaseURL,
		CredentialVersion: backupCredentialVersion, CredentialCount: credentials.Count,
		CredentialSHA256: credentials.SHA256,
	}, "", "  ")
	if err != nil {
		_ = closeArchive()
		cleanup()
		return "", "", func() {}, fmt.Errorf("生成备份元数据失败：%w", err)
	}
	metadataRaw = append(metadataRaw, '\n')
	metadataHeader := &tar.Header{Name: "backup.json", Mode: 0o600, Size: int64(len(metadataRaw))}
	if err := tw.WriteHeader(metadataHeader); err != nil {
		_ = closeArchive()
		cleanup()
		return "", "", func() {}, fmt.Errorf("写入备份元数据失败：%w", err)
	}
	if _, err := tw.Write(metadataRaw); err != nil {
		_ = closeArchive()
		cleanup()
		return "", "", func() {}, fmt.Errorf("写入备份元数据失败：%w", err)
	}

	manifest := backupManifest(a.version, a.cfg.BaseURL)
	mh := &tar.Header{Name: "MANIFEST.txt", Mode: 0o644, Size: int64(len(manifest))}
	if err := tw.WriteHeader(mh); err != nil {
		_ = closeArchive()
		cleanup()
		return "", "", func() {}, fmt.Errorf("写入备份清单失败：%w", err)
	}
	if _, err := io.WriteString(tw, manifest); err != nil {
		_ = closeArchive()
		cleanup()
		return "", "", func() {}, fmt.Errorf("写入备份清单失败：%w", err)
	}
	if err := closeArchive(); err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("关闭备份归档失败：%w", err)
	}
	return archivePath, archiveName, cleanup, nil
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
		"  backup.json       — 数据库版本、完整性和多用户节点凭据校验信息",
		"",
		"迁移到新服务器:",
		"  1. 用 install.sh 安装二进制版面板",
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
	releaseMaintenanceLock := true
	defer func() {
		if releaseMaintenanceLock {
			a.selfUpdating.Unlock()
		}
	}()

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
		latest, err := latestPanelRelease(true)
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
	webAssetName := "singbox-panel-web-" + target + ".tar.gz"
	agentAssetName := "singbox-panel-agents-" + target + ".tar.gz"
	base := "https://github.com/" + githubRepo + "/releases/download/" + target

	// Stage and validate the complete release before touching any live file.
	installDir := installDirFromDSN(a.cfg.Database.DSN)
	if installDir == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "数据库必须位于 <安装目录>/data/ 下才能使用面板内更新"})
		return
	}
	updateRoot := filepath.Join(installDir, updateSubdir)
	if err := os.MkdirAll(updateRoot, 0o700); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建暂存目录失败：" + err.Error()})
		return
	}
	updateLockPath := filepath.Join(updateRoot, ".active")
	if info, statErr := os.Stat(updateLockPath); statErr == nil {
		if time.Since(info.ModTime()) <= 30*time.Minute {
			c.JSON(http.StatusConflict, gin.H{"error": "已有更新任务仍在运行或等待验证"})
			return
		}
		_ = os.Remove(updateLockPath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "检查更新锁失败：" + statErr.Error()})
		return
	}
	lockFile, err := os.OpenFile(updateLockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			c.JSON(http.StatusConflict, gin.H{"error": "已有更新任务仍在运行或等待验证"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建更新锁失败：" + err.Error()})
		return
	}
	_, _ = fmt.Fprintf(lockFile, "%d\n", time.Now().Unix())
	_ = lockFile.Sync()
	_ = lockFile.Close()
	cleanupUpdateLock := true
	defer func() {
		if cleanupUpdateLock {
			_ = os.Remove(updateLockPath)
		}
	}()
	stageDir, err := os.MkdirTemp(updateRoot, target+"-")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建版本暂存目录失败：" + err.Error()})
		return
	}
	cleanupStage := true
	defer func() {
		if cleanupStage {
			_ = os.RemoveAll(stageDir)
		}
	}()
	stagedBin := filepath.Join(stageDir, assetName)
	stagedWebArchive := filepath.Join(stageDir, webAssetName)
	stagedAgentArchive := filepath.Join(stageDir, agentAssetName)
	rollbackDB := filepath.Join(stageDir, "database.rollback")
	dbPath, _ := a.sqliteDBPath()

	checksumManifest, err := downloadReleaseChecksums(base + "/checksums.txt")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	for _, asset := range []struct{ name, path string }{
		{assetName, stagedBin}, {webAssetName, stagedWebArchive}, {agentAssetName, stagedAgentArchive},
	} {
		if err := downloadFileLimit(base+"/"+asset.name, asset.path, maxUpdateAsset); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "下载 " + asset.name + " 失败：" + err.Error()})
			return
		}
		if err := verifyReleaseChecksumManifest(checksumManifest, asset.path, asset.name); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
	}
	if err := os.Chmod(stagedBin, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "设置可执行权限失败：" + err.Error()})
		return
	}
	versionCtx, cancelVersionCheck := context.WithTimeout(c.Request.Context(), 10*time.Second)
	out, versionErr := exec.CommandContext(versionCtx, stagedBin, "--version").CombinedOutput()
	cancelVersionCheck()
	if versionErr != nil || strings.TrimSpace(string(out)) != "singbox-panel "+target {
		c.JSON(http.StatusBadGateway, gin.H{"error": "新版二进制版本校验失败"})
		return
	}
	stagedWebDir := filepath.Join(stageDir, "web")
	stagedAgentsDir := filepath.Join(stageDir, "agents")
	if err := extractUpdateArchive(stagedWebArchive, stagedWebDir); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "解压前端失败：" + err.Error()})
		return
	}
	if err := extractUpdateArchive(stagedAgentArchive, stagedAgentsDir); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "解压 Agent 包失败：" + err.Error()})
		return
	}
	if !fileExists(filepath.Join(stagedWebDir, "index.html")) ||
		!fileExists(filepath.Join(stagedAgentsDir, "singbox-panel-agent-linux-amd64")) ||
		!fileExists(filepath.Join(stagedAgentsDir, "singbox-panel-agent-linux-arm64")) {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Release 前端或 Agent 包内容不完整"})
		return
	}
	quotedRollbackDB := strings.ReplaceAll(rollbackDB, "'", "''")
	if err := a.db.Exec("VACUUM INTO '" + quotedRollbackDB + "'").Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建更新前数据库快照失败：" + err.Error()})
		return
	}
	if err := os.Chmod(rollbackDB, 0o600); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保护数据库回滚快照失败：" + err.Error()})
		return
	}
	webDir, err := managedUpdatePath(a.cfg.WebDir, installDir)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "前端目录不支持面板内更新：" + err.Error()})
		return
	}
	agentsDir, err := managedUpdatePath(a.cfg.AgentsDir, installDir)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Agent 目录不支持面板内更新：" + err.Error()})
		return
	}
	if err := validateManagedUpdateTargets(installDir, updateRoot, webDir, agentsDir); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "更新目录配置不安全：" + err.Error()})
		return
	}
	readyURL, err := panelReadyURL(a.cfg.Listen)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "面板监听地址无法用于更新验证：" + err.Error()})
		return
	}

	// Detach one transactional binary+web+Agent swap. The helper retains every
	// old component and restores all of them if startup/readiness fails.
	if err := launchSwap(stagedBin, stagedWebDir, stagedAgentsDir, stageDir, webDir, agentsDir, readyURL, updateLockPath, dbPath, rollbackDB); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "启动更新任务失败：" + err.Error()})
		return
	}
	cleanupStage = false
	cleanupUpdateLock = false
	// The detached swap has ownership of the live installation now and will stop
	// this process. Keep the in-process maintenance gate locked until that happens;
	// otherwise a restore or second update can start in the hand-off window.
	releaseMaintenanceLock = false

	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"updated": true,
		"message": "已开始完整更新到 " + target + "，后端、前端和 Agent 包将一起切换并自动验证。",
		"version": target,
	})
}

// validReleaseTag allows only vX.Y.Z-style tags: digits, dots, dashes and a
// leading v. This value reaches both a URL and a shell command line.
var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z][0-9A-Za-z.-]*)?$`)

func validReleaseTag(s string) bool { return len(s) <= 64 && releaseTagPattern.MatchString(s) }

// installDirFromDSN derives the install directory from the SQLite DSN. The DB
// lives at <install>/data/singbox-panel.db, so the install dir is two levels up.
func installDirFromDSN(dsn string) string {
	dbPath := sqliteFilePath(dsn)
	if dbPath == "" {
		return ""
	}
	dbPath = filepath.Clean(dbPath)
	dataDir := filepath.Dir(dbPath)
	if filepath.Base(dataDir) != "data" {
		return ""
	}
	root := filepath.Dir(dataDir)
	if root == "." || root == string(filepath.Separator) {
		return ""
	}
	return root
}

func downloadFileLimit(url, dest string, maxBytes int64) error {
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
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if n > maxBytes {
		return fmt.Errorf("文件超过 %d 字节限制", maxBytes)
	}
	return f.Sync()
}

func downloadReleaseChecksums(checksumsURL string) ([]byte, error) {
	resp, err := httpGetGitHub(checksumsURL, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("下载 checksums.txt 失败：%w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("读取 checksums.txt 失败：%w", err)
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("checksums.txt 超过大小限制")
	}
	return raw, nil
}

// verifyReleaseChecksumManifest confirms one asset against the single manifest
// snapshot fetched for this update. Reusing one snapshot prevents a mutable
// release tag from combining assets verified against different manifest
// generations.
func verifyReleaseChecksumManifest(raw []byte, filePath, assetName string) error {
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
	if len(want) != sha256.Size*2 {
		return fmt.Errorf("checksums.txt 中 %s 的 SHA256 格式无效", assetName)
	}
	if _, err := hex.DecodeString(want); err != nil {
		return fmt.Errorf("checksums.txt 中 %s 的 SHA256 格式无效", assetName)
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

func managedUpdatePath(path, installDir string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("目录为空")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(installDir, path)
	}
	path = filepath.Clean(path)
	root := filepath.Clean(installDir)
	resolvedRoot, err := resolvePathForContainment(root)
	if err != nil {
		return "", fmt.Errorf("解析安装目录失败：%w", err)
	}
	resolvedPath, err := resolvePathForContainment(path)
	if err != nil {
		return "", fmt.Errorf("解析目标目录失败：%w", err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("目录必须位于安装目录 %s 内", resolvedRoot)
	}
	return path, nil
}

func resolvePathForContainment(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	resolvedParent, parentErr := filepath.EvalSymlinks(filepath.Dir(path))
	if parentErr != nil {
		return "", parentErr
	}
	return filepath.Join(resolvedParent, filepath.Base(path)), nil
}

func pathsOverlap(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if a == b {
		return true
	}
	relAB, errAB := filepath.Rel(a, b)
	relBA, errBA := filepath.Rel(b, a)
	inside := func(rel string, err error) bool {
		return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return inside(relAB, errAB) || inside(relBA, errBA)
}

func validateManagedUpdateTargets(installDir, updateRoot, webDir, agentsDir string) error {
	resolvedWeb, err := resolvePathForContainment(webDir)
	if err != nil {
		return err
	}
	resolvedAgents, err := resolvePathForContainment(agentsDir)
	if err != nil {
		return err
	}
	if pathsOverlap(resolvedWeb, resolvedAgents) {
		return errors.New("前端目录与 Agent 目录不能相同或互相包含")
	}
	for label, protected := range map[string]string{
		"数据目录": filepath.Join(installDir, "data"),
		"更新目录": updateRoot,
	} {
		resolvedProtected, resolveErr := resolvePathForContainment(protected)
		if resolveErr != nil {
			return resolveErr
		}
		if pathsOverlap(resolvedWeb, resolvedProtected) || pathsOverlap(resolvedAgents, resolvedProtected) {
			return fmt.Errorf("前端或 Agent 目录与%s重叠", label)
		}
	}
	return nil
}

func panelReadyURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return "", err
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/api/ready"}).String(), nil
}

func extractUpdateArchive(archivePath, destination string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	limited := &io.LimitedReader{R: gz, N: maxUpdateAsset + 1}
	tr := tar.NewReader(limited)
	seen := make(map[string]bool)
	var total int64
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		entries++
		if entries > 20_000 {
			return fmt.Errorf("归档条目过多")
		}
		name := filepath.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if name == "." || name == "" {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("归档包含不安全路径 %q", hdr.Name)
		}
		target := filepath.Join(destination, name)
		rel, err := filepath.Rel(destination, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("归档路径越界 %q", hdr.Name)
		}
		if seen[target] {
			return fmt.Errorf("归档包含重复路径 %q", hdr.Name)
		}
		seen[target] = true
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if hdr.Size < 0 || hdr.Size > maxUpdateAsset-total {
				return fmt.Errorf("归档解压后超过大小限制")
			}
			total += hdr.Size
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(out, tr, hdr.Size)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("归档包含不支持的条目类型 %d", hdr.Typeflag)
		}
	}
	// tar stops at its end marker. Drain gzip so checksum/truncation errors and
	// oversized decompressed tails cannot be hidden after an otherwise valid tar.
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return err
	}
	if limited.N <= 0 {
		return fmt.Errorf("归档解压后超过大小限制")
	}
	return nil
}

// launchSwap runs a complete release swap in a transient root unit. It keeps
// the old binary, frontend, and Agent bundle together and restores all three
// unless the restarted panel passes /api/ready.
func launchSwap(stagedBin, stagedWebDir, stagedAgentsDir, stageDir, webDir, agentsDir, readyURL, lockPath, dbPath, rollbackDB string) error {
	script := buildSwapScript(panelBinaryPath(), stagedBin, stagedWebDir, stagedAgentsDir, stageDir, webDir, agentsDir, readyURL, lockPath, dbPath, rollbackDB)

	cmd := exec.Command("systemd-run",
		"--collect",
		"--unit", fmt.Sprintf("singbox-panel-selfupdate-%d", time.Now().UnixNano()),
		"/bin/sh", "-c", script,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func buildSwapScript(liveBin, stagedBin, stagedWebDir, stagedAgentsDir, stageDir, webDir, agentsDir, readyURL, lockPath, dbPath, rollbackDB string) string {
	return fmt.Sprintf(`set -u
sleep 2
STAGE=%[1]s
ROLLBACK="$STAGE/rollback"
BIN=%[2]s
WEB=%[3]s
AGENTS=%[4]s
NEWBIN=%[5]s
NEWWEB=%[6]s
NEWAGENTS=%[7]s
SERVICE=%[8]s
READY=%[9]s
LOCK=%[10]s
DB=%[11]s
ROLLBACK_DB=%[12]s
WEB_MOVED=0
AGENTS_MOVED=0
NEW_WEB_INSTALLED=0
NEW_AGENTS_INSTALLED=0
ROLLBACK_FAILED=0
trap 'rm -f -- "$LOCK"' EXIT

rollback_update() {
  ROLLBACK_FAILED=0
  if ! systemctl stop "$SERVICE" >/dev/null 2>&1 && systemctl is-active --quiet "$SERVICE"; then
    echo "rollback: unable to stop $SERVICE" >&2
    return 1
  fi
  rm -f -- "$BIN.new" || ROLLBACK_FAILED=1
  if [ "$NEW_WEB_INSTALLED" -eq 1 ]; then rm -rf -- "$WEB" || ROLLBACK_FAILED=1; fi
  if [ "$NEW_AGENTS_INSTALLED" -eq 1 ]; then rm -rf -- "$AGENTS" || ROLLBACK_FAILED=1; fi
  if [ "$WEB_MOVED" -eq 1 ]; then
    if [ -e "$ROLLBACK/web" ]; then
      mkdir -p "$(dirname "$WEB")" && mv "$ROLLBACK/web" "$WEB" || ROLLBACK_FAILED=1
    else
      ROLLBACK_FAILED=1
    fi
  fi
  if [ "$AGENTS_MOVED" -eq 1 ]; then
    if [ -e "$ROLLBACK/agents" ]; then
      mkdir -p "$(dirname "$AGENTS")" && mv "$ROLLBACK/agents" "$AGENTS" || ROLLBACK_FAILED=1
    else
      ROLLBACK_FAILED=1
    fi
  fi
  if [ -f "$ROLLBACK/panel" ]; then
    install -m 0755 "$ROLLBACK/panel" "$BIN" || ROLLBACK_FAILED=1
  else
    ROLLBACK_FAILED=1
  fi
  rm -f -- "$DB-wal" "$DB-shm" "$DB.rollback-new" || ROLLBACK_FAILED=1
  if [ -f "$ROLLBACK_DB" ]; then
    if cp -p -- "$ROLLBACK_DB" "$DB.rollback-new"; then
      chmod 0600 "$DB.rollback-new" && mv -f "$DB.rollback-new" "$DB" || ROLLBACK_FAILED=1
    else
      rm -f -- "$DB.rollback-new"
      ROLLBACK_FAILED=1
    fi
  else
    ROLLBACK_FAILED=1
  fi
  if [ "$ROLLBACK_FAILED" -eq 0 ]; then
    systemctl restart "$SERVICE" >/dev/null 2>&1 || ROLLBACK_FAILED=1
  fi
  if [ "$ROLLBACK_FAILED" -ne 0 ]; then
    echo "rollback failed; recovery files remain in $STAGE" >&2
    return 1
  fi
  return 0
}

perform_update() {
  systemctl stop "$SERVICE" || return 1
  if [ -e "$WEB" ]; then mv "$WEB" "$ROLLBACK/web" || return 1; WEB_MOVED=1; fi
  if [ -e "$AGENTS" ]; then mv "$AGENTS" "$ROLLBACK/agents" || return 1; AGENTS_MOVED=1; fi
  mkdir -p "$(dirname "$WEB")" "$(dirname "$AGENTS")" || return 1
  mv "$NEWWEB" "$WEB" || return 1
  NEW_WEB_INSTALLED=1
  mv "$NEWAGENTS" "$AGENTS" || return 1
  NEW_AGENTS_INSTALLED=1
  install -m 0755 "$NEWBIN" "$BIN.new" || return 1
  mv -f "$BIN.new" "$BIN" || return 1
  systemctl restart "$SERVICE" || return 1
}

mkdir -p "$ROLLBACK"
cp -p "$BIN" "$ROLLBACK/panel" || exit 1
if ! perform_update; then
  if rollback_update; then rm -rf -- "$STAGE"; fi
  exit 1
fi

i=0
while [ "$i" -lt 30 ]; do
  if systemctl is-active --quiet "$SERVICE"; then
    if command -v curl >/dev/null 2>&1 && curl -fsS --max-time 3 "$READY" >/dev/null 2>&1; then
      rm -rf -- "$STAGE"
      exit 0
    fi
    if command -v wget >/dev/null 2>&1 && wget -qO- -T 3 "$READY" >/dev/null 2>&1; then
      rm -rf -- "$STAGE"
      exit 0
    fi
  fi
  i=$((i + 1))
  sleep 2
done
if rollback_update; then rm -rf -- "$STAGE"; fi
exit 1`,
		shellQuote(stageDir), shellQuote(liveBin), shellQuote(webDir), shellQuote(agentsDir),
		shellQuote(stagedBin), shellQuote(stagedWebDir), shellQuote(stagedAgentsDir),
		shellQuote(serviceUnitName), shellQuote(readyURL), shellQuote(lockPath), shellQuote(dbPath), shellQuote(rollbackDB))
}

// panelBinaryPath returns the path to the running panel binary so the swap
// overwrites the correct file even for a non-standard install.
func panelBinaryPath() string {
	if p, err := os.Executable(); err == nil && p != "" {
		return p
	}
	return "/usr/local/bin/singbox-panel"
}
