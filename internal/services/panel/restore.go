package panel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

// Backup restore: import a .tar.gz produced by downloadBackup, replacing the
// live SQLite database and (optionally) the jwt_secret. This is the inverse of
// the migration we support out of band — restoring on a fresh host and pointing
// the same domain at it makes every agent reconnect automatically, because the
// agent tokens live in the database and sessions survive when jwt_secret does.
//
// Doing this in-process is delicate: we hold the DB open and cannot simply
// overwrite the file underneath GORM. So we stage the incoming DB, validate it,
// swap the files with the service briefly stopped by systemd — but since the
// panel manages its own lifecycle here, we instead write the new DB into place
// and require a restart to pick it up. To keep it safe we:
//  1. snapshot the current DB to *.pre-restore-<ts> first (rollback),
//  2. write the imported DB atomically next to the live file,
//  3. persist jwt_secret into panel.yaml or the data-dir sidecar,
//  4. tell the client to expect a restart, then exit so systemd restarts us
//     cleanly against the restored files.

const (
	maxBackupUpload       = 512 << 20 // 512 MiB ceiling on the uploaded archive
	maxBackupRequest      = maxBackupUpload + 1<<20
	maxBackupDatabase     = 512 << 20
	maxBackupSecret       = 4096
	maxBackupPayload      = maxBackupDatabase + 1<<20
	maxBackupUncompressed = maxBackupDatabase + 16<<20
	maxRestoreRollbacks   = 5
	backupUploadTimeout   = 30 * time.Minute
	sqliteMagic           = "SQLite format 3\x00"
)

// POST /api/admin/maintenance/restore — multipart upload field "file".
func (a *App) restoreBackup(c *gin.Context) {
	// Restore only makes sense for the file-backed SQLite deployment.
	_, ok := a.sqliteDBPath()
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅 SQLite 部署支持一键恢复"})
		return
	}

	// Serialize against self-update; both mutate files under the install dir.
	if ok := a.selfUpdating.TryLock(); !ok {
		c.JSON(http.StatusConflict, gin.H{"error": "已有维护任务正在进行"})
		return
	}
	releaseMaintenanceLock := true
	defer func() {
		if releaseMaintenanceLock {
			a.selfUpdating.Unlock()
		}
	}()

	// The general HTTP read timeout is intentionally short for JSON APIs. A
	// legitimate data-only backup can be hundreds of MiB, so extend only this
	// authenticated upload's connection deadline while retaining strict byte
	// limits below. Gin exposes the underlying writer through Unwrap, which lets
	// ResponseController reach the server connection on real requests; test
	// recorders may not support it, and safely return ErrNotSupported.
	_ = http.NewResponseController(c.Writer).SetReadDeadline(time.Now().Add(backupUploadTimeout))

	// Stream the multipart part directly into the restore parser. FormFile would
	// spool uploads larger than 32 MiB into /tmp, which is deliberately small in
	// the hardened service and unnecessary because the restore parser already
	// stages the database in the data directory.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBackupRequest)
	multipartReader, err := c.Request.MultipartReader()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 multipart 上传"})
		return
	}
	for parts := 0; parts < 32; parts++ {
		part, partErr := multipartReader.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil {
			var maxErr *http.MaxBytesError
			if errors.As(partErr, &maxErr) {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "备份文件过大"})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "读取上传失败：" + partErr.Error()})
			return
		}
		if part.FormName() == "file" && part.FileName() != "" {
			restartScheduled := a.restoreBackupReader(c, io.LimitReader(part, maxBackupUpload+1))
			releaseMaintenanceLock = !restartScheduled
			_ = part.Close()
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(part, 64<<10))
		_ = part.Close()
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "缺少上传文件（字段名 file）"})
}

// restoreBackupReader is shared by browser uploads and direct OneDrive
// restores. It validates and migrates the staged copy completely before the
// live database is closed or renamed.
func (a *App) restoreBackupReader(c *gin.Context, src io.Reader) (restartScheduled bool) {
	dbPath, ok := a.sqliteDBPath()
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅 SQLite 部署支持一键恢复"})
		return
	}

	stageDir, err := os.MkdirTemp(filepath.Dir(dbPath), ".restore-")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建暂存目录失败：" + err.Error()})
		return
	}
	defer os.RemoveAll(stageDir)

	stagedDB, importedSecret, metadata, metadataPresent, err := extractBackupWithMetadata(src, stageDir)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "解析备份失败：" + err.Error()})
		return
	}
	if stagedDB == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "备份中未找到 singbox-panel.db"})
		return
	}
	if err := validateSQLiteFile(stagedDB); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if metadataPresent {
		if err := validateBackupMetadata(metadata, stagedDB, a.version); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "备份版本或完整性校验失败：" + err.Error()})
			return
		}
	}

	// Capture the currently valid cloud authorization before the live DB is
	// closed. It will be re-encrypted with the secret that the restored process
	// will actually use, so a restore does not require another OneDrive login.
	currentCloud := defaultOneDriveSettings()
	if loadedCloud, loadErr := a.loadOneDriveSettings(); loadErr != nil {
		// A broken cloud setting must not make the local disaster-recovery path
		// unusable. Leave the archived setting untouched; with its matching
		// jwt_secret it may still become usable after restart.
		log.Printf("restore: skip preserving unreadable OneDrive authorization: %v", loadErr)
	} else {
		currentCloud = loadedCloud
	}
	beforeFingerprint, err := fingerprintFromPath(stagedDB)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "校验节点凭据失败：" + err.Error()})
		return
	}

	// Run all pending migrations against the staged file. The migration runner
	// creates only temporary pre-migration snapshots inside stageDir; none can
	// affect the live database.
	staged, err := openSQLiteForMigration(stagedDB)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "打开暂存数据库失败：" + err.Error()})
		return
	}
	stagedCfg := a.cfg.Database
	stagedCfg.Driver = "sqlite"
	stagedCfg.DSN = stagedDB
	if err := runSchemaMigrations(staged, stagedCfg); err != nil {
		if sqlDB, closeErr := staged.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "备份需要升级但迁移失败：" + err.Error()})
		return
	}
	afterFingerprint, err := calculateCredentialFingerprint(staged)
	if err != nil {
		if sqlDB, closeErr := staged.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "迁移后校验节点凭据失败：" + err.Error()})
		return
	}
	if err := validateRestoredAdministrators(staged); err != nil {
		if sqlDB, closeErr := staged.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "管理员账户预检失败：" + err.Error()})
		return
	}
	if beforeFingerprint.Available && afterFingerprint.Available && beforeFingerprint.SHA256 != afterFingerprint.SHA256 {
		if sqlDB, closeErr := staged.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "数据库迁移改变了多用户节点凭据，已拒绝恢复"})
		return
	}
	if metadataPresent && (metadata.CredentialSHA256 != afterFingerprint.SHA256 || metadata.CredentialCount != afterFingerprint.Count) {
		if sqlDB, closeErr := staged.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "备份中的多用户节点凭据校验失败，已拒绝恢复"})
		return
	}

	runtimeSecret, secretWarning := restoreRuntimeJWTSecret(a, importedSecret)
	if runtimeSecret == "" {
		if sqlDB, closeErr := staged.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "无法确定恢复后使用的 jwt_secret"})
		return
	}
	if isWeakSecret(runtimeSecret) {
		if sqlDB, closeErr := staged.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "恢复后使用的 jwt_secret 不符合安全要求"})
		return
	}
	if err := preflightRestoredJWTSecretPersistence(a.cfgPath, dbPath, runtimeSecret); err != nil {
		if sqlDB, closeErr := staged.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "恢复前检查 jwt_secret 持久化失败：" + err.Error()})
		return
	}
	if currentCloud.RefreshToken != "" {
		stagedApp := &App{cfg: a.cfg, db: staged}
		stagedApp.cfg.JWTSecret = runtimeSecret
		if err := stagedApp.saveOneDriveSettings(currentCloud); err != nil {
			if sqlDB, closeErr := staged.DB(); closeErr == nil {
				_ = sqlDB.Close()
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "保留 OneDrive 授权失败：" + err.Error()})
			return
		}
	}
	if sqlDB, err := staged.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// From here on the archive is trusted. Do the destructive work in an order
	// that is safe even if a step fails partway:
	//
	//  1) rollback snapshot via VACUUM INTO while the live handle is still open
	//     (a plain file copy would miss un-checkpointed WAL frames);
	//  2) CLOSE the live DB connection pool — this is essential. SQLite keeps
	//     the file open by descriptor; if we rename a new file over the path
	//     while the pool still holds the old inode (and its -wal), the running
	//     process keeps reading the old data and flushes stale WAL frames back
	//     on shutdown, silently undoing the restore. Closing first releases the
	//     descriptors and the WAL/SHM sidecars;
	//  3) drop any -wal/-shm, then rename the imported DB into place;
	//  4) rewrite jwt_secret; 5) reply; 6) restart so a fresh process opens the
	//     restored file.

	// 1) rollback snapshot (consistent even under WAL).
	rollback := fmt.Sprintf("%s.pre-restore-%s", dbPath, time.Now().UTC().Format("20060102T150405.000000000Z"))
	if _, statErr := os.Stat(dbPath); statErr == nil {
		quoted := strings.ReplaceAll(rollback, "'", "''")
		if err := a.db.Exec("VACUUM INTO '" + quoted + "'").Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "备份现有数据库失败：" + err.Error()})
			return
		}
		if err := os.Chmod(rollback, 0o600); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保护现有数据库快照失败：" + err.Error()})
			return
		}
	}

	// 2) close the live connection pool so no descriptor pins the old inode.
	if sqlDB, err := a.db.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// 3) remove stale journals, then move the imported DB into place. Both files
	// live in the same directory, so the rename is atomic.
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(dbPath + suffix)
	}
	if err := os.Rename(stagedDB, dbPath); err != nil {
		if err2 := copyFile(stagedDB, dbPath); err2 != nil {
			// The live pool is already closed and the DB may be gone; the safest
			// recovery is to restore the rollback snapshot and restart.
			rollbackErr := copyFile(rollback, dbPath)
			message := "写入恢复数据库失败：" + err2.Error()
			if rollbackErr != nil {
				message += "；原数据库回滚也失败：" + rollbackErr.Error()
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": message})
			restartScheduled = true
			a.scheduleRestart()
			return
		}
	}

	// 4) Persist the exact secret selected during preflight. It may intentionally
	// differ from a legacy/unsafe imported secret, or be fixed by an environment
	// variable that the running process cannot rewrite.
	secretApplied, restoreWarning := persistRestoredJWTSecret(a.cfgPath, dbPath, runtimeSecret)
	if !secretApplied {
		rollbackErr := copyFile(rollback, dbPath)
		message := "jwt_secret 持久化失败，已恢复原数据库：" + restoreWarning
		if rollbackErr != nil {
			message = "jwt_secret 持久化失败，且原数据库回滚失败：" + restoreWarning + "；" + rollbackErr.Error()
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": message})
		restartScheduled = true
		a.scheduleRestart()
		return
	}
	if restoreWarning == "" {
		restoreWarning = secretWarning
	}
	if restoreWarning != "" {
		c.Header("X-Restore-Warning", restoreWarning)
	}
	if err := pruneRestoreRollbacks(dbPath, maxRestoreRollbacks); err != nil {
		log.Printf("prune pre-restore database snapshots: %v", err)
	}

	msg := "恢复完成，面板即将重启以加载导入的数据。"
	if restoreWarning != "" {
		msg = "数据已恢复。注意：" + restoreWarning
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":             true,
		"restarting":     true,
		"secret_applied": secretApplied,
		"rollback_file":  rollback,
		"message":        msg,
	})

	// The live DB pool is already closed; this process can no longer serve
	// requests against the database. Restart unconditionally so a fresh process
	// opens the restored file. Under systemd (Restart=always) this is seamless;
	// elsewhere the supervisor is expected to bring us back.
	restartScheduled = true
	a.scheduleRestart()
	return
}

func validateRestoredAdministrators(db *gorm.DB) error {
	var admins []model.User
	if err := db.Where("role = ?", model.RoleAdmin).Find(&admins).Error; err != nil {
		return err
	}
	active := 0
	for i := range admins {
		if !userActive(&admins[i]) {
			continue
		}
		active++
		if _, err := bcrypt.Cost([]byte(admins[i].Password)); err != nil {
			return fmt.Errorf("管理员 %q 的密码哈希无效", admins[i].Email)
		}
	}
	if active == 0 {
		return errors.New("备份中没有已启用且未到期的管理员账户")
	}
	return nil
}

func fingerprintFromPath(path string) (credentialFingerprint, error) {
	db, err := openSQLiteReadOnly(path)
	if err != nil {
		return credentialFingerprint{}, err
	}
	fingerprint, fingerprintErr := calculateCredentialFingerprint(db)
	if sqlDB, closeErr := db.DB(); closeErr == nil {
		_ = sqlDB.Close()
	}
	return fingerprint, fingerprintErr
}

func openSQLiteForMigration(path string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(sqliteDSN(path)), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
	}
	return db, nil
}

func restoreRuntimeJWTSecret(a *App, imported string) (string, string) {
	for _, name := range []string{"JWT_SECRET", "SINGBOX_PANEL_JWT_SECRET"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			if imported != "" && value != imported {
				return value, name + " 环境变量优先于备份中的 jwt_secret，已保留当前密钥；备份中的登录会话不会迁移"
			}
			return value, ""
		}
	}
	imported = strings.TrimSpace(imported)
	if imported != "" && !isWeakSecret(imported) {
		return imported, ""
	}
	secret, err := ResolveJWTSecret(a.cfg)
	if err != nil {
		return "", err.Error()
	}
	if isWeakSecret(secret) {
		secret = randHex(32)
	}
	if imported != "" {
		return secret, "备份中的 jwt_secret 不符合当前安全要求，已保留当前安全密钥；现有登录会话需要重新登录"
	}
	return secret, "备份未包含 jwt_secret，已保留当前安全密钥；原登录会话不会迁移"
}

// preflightRestoredJWTSecretPersistence verifies that the selected runtime key
// can still be used after restart before the live database handle is closed.
// Environment-backed secrets require no filesystem mutation. File-backed
// deployments prove write access without changing the current key.
func preflightRestoredJWTSecretPersistence(cfgPath, dbPath, secret string) error {
	if strings.TrimSpace(secret) == "" {
		return errors.New("jwt_secret 为空")
	}
	for _, name := range []string{"JWT_SECRET", "SINGBOX_PANEL_JWT_SECRET"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			if value != secret {
				return fmt.Errorf("%s 环境变量与恢复时选择的 jwt_secret 不一致", name)
			}
			return nil
		}
	}
	if cfgPath != "" {
		f, err := os.OpenFile(cfgPath, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		return f.Close()
	}
	dir := filepath.Dir(dbPath)
	f, err := os.CreateTemp(dir, ".jwt-secret-preflight-")
	if err != nil {
		return err
	}
	path := f.Name()
	if closeErr := f.Close(); closeErr != nil {
		_ = os.Remove(path)
		return closeErr
	}
	return os.Remove(path)
}

// scheduleRestart exits the process shortly after, giving the HTTP response time
// to flush. systemd Restart=always brings
// the panel back up against whatever is now on disk.
//
// Overridable so tests can assert a restart was requested without the test
// binary calling os.Exit on itself.
var scheduleRestartFn = func() {
	go func() {
		time.Sleep(800 * time.Millisecond)
		os.Exit(0)
	}()
}

func (a *App) scheduleRestart() { scheduleRestartFn() }

// extractBackup reads a gzip tar, writing singbox-panel.db into stageDir and
// returning its path plus the jwt_secret contents (if present). Entry names are
// sanitized to their base name to defeat path traversal.
func extractBackup(r io.Reader, stageDir string) (dbPath, secret string, err error) {
	dbPath, secret, _, _, err = extractBackupWithMetadata(r, stageDir)
	return dbPath, secret, err
}

func extractBackupWithMetadata(r io.Reader, stageDir string) (dbPath, secret string, metadata backupMetadata, metadataPresent bool, err error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", "", backupMetadata{}, false, fmt.Errorf("不是有效的 gzip 文件")
	}
	defer gz.Close()
	limited := &io.LimitedReader{R: gz, N: maxBackupUncompressed + 1}
	tr := tar.NewReader(limited)
	// Defensive cap on archive entries: only the two known files are ever
	// written, but a pathological archive with a huge number of headers would
	// otherwise force an unbounded stream scan.
	const maxEntries = 10_000
	entries := 0
	var payloadSize int64
	seenDB := false
	seenSecret := false
	seenMetadata := false
	for {
		hdr, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", "", backupMetadata{}, false, e
		}
		entries++
		if entries > maxEntries {
			return "", "", backupMetadata{}, false, fmt.Errorf("备份归档条目过多（> %d）", maxEntries)
		}
		if hdr.Size < 0 || hdr.Size > maxBackupPayload-payloadSize {
			return "", "", backupMetadata{}, false, fmt.Errorf("备份归档解压后内容过大")
		}
		payloadSize += hdr.Size
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Base(hdr.Name) // strip any directory / traversal
		switch name {
		case "singbox-panel.db":
			if seenDB {
				return "", "", backupMetadata{}, false, fmt.Errorf("备份中包含重复的 singbox-panel.db")
			}
			seenDB = true
			if hdr.Size > maxBackupDatabase {
				return "", "", backupMetadata{}, false, fmt.Errorf("备份中的数据库文件过大")
			}
			dst := filepath.Join(stageDir, "singbox-panel.db")
			f, e := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if e != nil {
				return "", "", backupMetadata{}, false, e
			}
			if _, e := io.CopyN(f, tr, hdr.Size); e != nil {
				_ = f.Close()
				return "", "", backupMetadata{}, false, e
			}
			if e := f.Close(); e != nil {
				return "", "", backupMetadata{}, false, e
			}
			dbPath = dst
		case "jwt_secret":
			if seenSecret {
				return "", "", backupMetadata{}, false, fmt.Errorf("备份中包含重复的 jwt_secret")
			}
			seenSecret = true
			if hdr.Size > maxBackupSecret {
				return "", "", backupMetadata{}, false, fmt.Errorf("备份中的 jwt_secret 过大")
			}
			b := make([]byte, int(hdr.Size))
			_, e := io.ReadFull(tr, b)
			if e != nil {
				return "", "", backupMetadata{}, false, e
			}
			secret = strings.TrimSpace(string(b))
		case "backup.json":
			if seenMetadata {
				return "", "", backupMetadata{}, false, fmt.Errorf("备份中包含重复的 backup.json")
			}
			seenMetadata = true
			if hdr.Size > 64<<10 {
				return "", "", backupMetadata{}, false, fmt.Errorf("备份元数据过大")
			}
			b := make([]byte, int(hdr.Size))
			if _, e := io.ReadFull(tr, b); e != nil {
				return "", "", backupMetadata{}, false, e
			}
			if e := json.Unmarshal(b, &metadata); e != nil {
				return "", "", backupMetadata{}, false, fmt.Errorf("backup.json 格式无效：%w", e)
			}
			metadataPresent = true
		}
	}
	// tar.Reader stops at the end-of-archive marker. Drain the gzip stream so a
	// truncated checksum or an oversized decompressed tail is not silently
	// accepted.
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return "", "", backupMetadata{}, false, err
	}
	if limited.N <= 0 {
		return "", "", backupMetadata{}, false, fmt.Errorf("备份归档解压后内容过大")
	}
	return dbPath, secret, metadata, metadataPresent, nil
}

func validateBackupMetadata(metadata backupMetadata, stagedDB, currentVersion string) error {
	if metadata.FormatVersion != backupFormatVersion {
		if metadata.FormatVersion > backupFormatVersion {
			return fmt.Errorf("备份格式版本 %d 高于当前支持的 %d，请先升级面板", metadata.FormatVersion, backupFormatVersion)
		}
		return fmt.Errorf("备份格式版本无效：%d", metadata.FormatVersion)
	}
	if metadata.DatabaseDriver != "sqlite" {
		return fmt.Errorf("不支持的备份数据库类型：%s", metadata.DatabaseDriver)
	}
	if metadata.DatabaseSHA256 == "" || len(metadata.DatabaseSHA256) != 64 {
		return fmt.Errorf("缺少数据库完整性校验值")
	}
	if _, err := hex.DecodeString(metadata.DatabaseSHA256); err != nil {
		return fmt.Errorf("数据库完整性校验值无效")
	}
	hash, err := hashFile(stagedDB)
	if err != nil {
		return fmt.Errorf("计算数据库完整性校验值失败：%w", err)
	}
	if !strings.EqualFold(hash, metadata.DatabaseSHA256) {
		return fmt.Errorf("数据库文件校验值不匹配")
	}
	if metadata.SchemaVersion == 0 {
		return fmt.Errorf("缺少数据库版本")
	}
	if metadata.SchemaVersion > currentSchemaVersion() {
		return fmt.Errorf("备份数据库版本 %d 高于当前支持的 %d，请先升级面板", metadata.SchemaVersion, currentSchemaVersion())
	}
	if currentVersion != "" && metadata.PanelVersion != "" {
		comparison, err := comparePanelVersions(metadata.PanelVersion, currentVersion)
		if err != nil {
			return err
		}
		if comparison > 0 {
			return fmt.Errorf("备份来自更高版本 %s，当前面板为 %s，请先升级面板", metadata.PanelVersion, currentVersion)
		}
	}
	if metadata.CredentialVersion != backupCredentialVersion {
		return fmt.Errorf("不支持的节点凭据校验版本：%d", metadata.CredentialVersion)
	}
	if metadata.CredentialCount < 0 || metadata.CredentialSHA256 == "" || len(metadata.CredentialSHA256) != 64 {
		return fmt.Errorf("缺少节点凭据校验信息")
	}
	if _, err := hex.DecodeString(metadata.CredentialSHA256); err != nil {
		return fmt.Errorf("节点凭据校验值无效")
	}
	actualSchema, err := func() (uint, error) {
		db, err := openSQLiteReadOnly(stagedDB)
		if err != nil {
			return 0, err
		}
		version, versionErr := schemaVersionInDB(db)
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
		return version, versionErr
	}()
	if err != nil {
		return fmt.Errorf("读取备份数据库版本失败：%w", err)
	}
	if actualSchema != metadata.SchemaVersion {
		return fmt.Errorf("备份元数据版本与数据库记录不一致")
	}
	return nil
}

// validateSQLiteFile confirms the staged file is a healthy Panel SQLite
// database whose migration history can be safely consumed by this binary.
func validateSQLiteFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, len(sqliteMagic))
	if _, err := io.ReadFull(f, buf); err != nil {
		return fmt.Errorf("备份中的数据库文件无法读取")
	}
	if string(buf) != sqliteMagic {
		return fmt.Errorf("备份中的 singbox-panel.db 不是有效的 SQLite 文件")
	}
	if err := f.Close(); err != nil {
		return err
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("解析备份数据库路径失败：%w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(absPath), RawQuery: "mode=ro"}).String()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return fmt.Errorf("备份中的数据库无法打开：%w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("备份中的数据库无法打开：%w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	defer sqlDB.Close()

	rows, err := sqlDB.Query("PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("备份数据库完整性检查失败：%w", err)
	}
	integrityOK := false
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			rows.Close()
			return fmt.Errorf("备份数据库完整性检查失败：%w", err)
		}
		if result != "ok" {
			rows.Close()
			return fmt.Errorf("备份数据库已损坏：%s", result)
		}
		integrityOK = true
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("备份数据库完整性检查失败：%w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("备份数据库完整性检查失败：%w", err)
	}
	if !integrityOK {
		return fmt.Errorf("备份数据库完整性检查未返回结果")
	}

	for _, table := range []string{"users", "servers"} {
		var count int
		if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count).Error; err != nil {
			return fmt.Errorf("检查备份数据库结构失败：%w", err)
		}
		if count != 1 {
			return fmt.Errorf("备份中的数据库不是 SingBox Panel 数据库：缺少 %s 表", table)
		}
	}

	migrations, err := validateMigrations(applicationMigrations)
	if err != nil {
		return fmt.Errorf("面板迁移定义无效：%w", err)
	}
	if _, _, err := validateSchemaMigrationHistory(db, migrations); err != nil {
		return fmt.Errorf("备份数据库迁移历史无效：%w", err)
	}
	return nil
}

var jwtSecretLine = regexp.MustCompile(`(?m)^jwt_secret:.*$`)

// rewriteJWTSecret replaces (or appends) the jwt_secret line in panel.yaml,
// preserving the rest of the file and encoding arbitrary values safely.
func rewriteJWTSecret(cfgPath, secret string) error {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	info, err := os.Stat(cfgPath)
	if err != nil {
		return err
	}
	line := "jwt_secret: " + strconv.Quote(secret)
	var out string
	if jwtSecretLine.Match(raw) {
		out = jwtSecretLine.ReplaceAllStringFunc(string(raw), func(string) string { return line })
	} else {
		out = strings.TrimRight(string(raw), "\n") + "\n" + line + "\n"
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o600
	}
	if atomicErr := writeFileAtomic(cfgPath, []byte(out), mode); atomicErr == nil {
		return nil
	} else if inPlaceErr := rewriteFileInPlace(cfgPath, raw, []byte(out)); inPlaceErr != nil {
		return fmt.Errorf("atomic rewrite failed: %v; in-place rewrite failed: %w", atomicErr, inPlaceErr)
	}
	return nil
}

// rewriteFileInPlace is the fallback for hardened systemd installs where the
// unit may write panel.yaml itself but its parent directory remains read-only.
// Padding a shorter replacement to the old length avoids a truncate window;
// trailing YAML whitespace is benign.
func rewriteFileInPlace(path string, original, replacement []byte) error {
	payload := append([]byte(nil), replacement...)
	if len(payload) < len(original) {
		payload = append(payload, bytes.Repeat([]byte{' '}, len(original)-len(payload))...)
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	writeErr := func() error {
		if _, err := f.WriteAt(payload, 0); err != nil {
			return err
		}
		if err := f.Truncate(int64(len(payload))); err != nil {
			return err
		}
		return f.Sync()
	}()
	closeErr := f.Close()
	if writeErr == nil && closeErr == nil {
		return nil
	}

	// Best-effort restoration for ordinary I/O failures. A sudden host power loss
	// is handled by the operator's existing database/config backup policy.
	if restore, restoreErr := os.OpenFile(path, os.O_WRONLY, 0); restoreErr == nil {
		_, _ = restore.WriteAt(original, 0)
		_ = restore.Truncate(int64(len(original)))
		_ = restore.Sync()
		_ = restore.Close()
	}
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// persistRestoredJWTSecret writes the imported signing key to the active
// persistence mechanism. An explicitly supplied environment variable cannot
// be changed by the running process, so a mismatch is reported rather than
// falsely claiming that the imported key will be used after restart.
func persistRestoredJWTSecret(cfgPath, dbPath, secret string) (bool, string) {
	if secret == "" {
		return false, ""
	}
	for _, name := range []string{"JWT_SECRET", "SINGBOX_PANEL_JWT_SECRET"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			if value == secret {
				return true, ""
			}
			return false, name + " 环境变量与备份中的 jwt_secret 不一致，请更新该环境变量后重启"
		}
	}
	if cfgPath != "" {
		if err := rewriteJWTSecret(cfgPath, secret); err != nil {
			return false, "jwt_secret 写入配置失败：" + err.Error()
		}
		return true, ""
	}
	secretPath := filepath.Join(filepath.Dir(dbPath), jwtSecretFile)
	if err := writeFileAtomic(secretPath, []byte(secret+"\n"), 0o600); err != nil {
		return false, "jwt_secret 写入数据目录失败：" + err.Error()
	}
	return true, ""
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func pruneRestoreRollbacks(dbPath string, keep int) error {
	dir := filepath.Dir(dbPath)
	prefix := filepath.Base(dbPath) + ".pre-restore-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type rollbackEntry struct {
		path    string
		modTime time.Time
	}
	rollbacks := make([]rollbackEntry, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rollbacks = append(rollbacks, rollbackEntry{
			path:    filepath.Join(dir, entry.Name()),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(rollbacks, func(i, j int) bool {
		if rollbacks[i].modTime.Equal(rollbacks[j].modTime) {
			return rollbacks[i].path > rollbacks[j].path
		}
		return rollbacks[i].modTime.After(rollbacks[j].modTime)
	})
	if keep < 0 {
		keep = 0
	}
	for _, rollback := range rollbacks[min(keep, len(rollbacks)):] {
		if err := os.Remove(rollback.path); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".copy-")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o600
	}
	if err := out.Chmod(mode); err != nil {
		_ = out.Close()
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
