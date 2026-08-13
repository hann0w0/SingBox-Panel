package panel

import (
	"archive/tar"
	"compress/gzip"
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
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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
	sqliteMagic           = "SQLite format 3\x00"
)

// POST /api/admin/maintenance/restore — multipart upload field "file".
func (a *App) restoreBackup(c *gin.Context) {
	// Restore only makes sense for the file-backed SQLite deployment.
	dbPath, ok := a.sqliteDBPath()
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅 SQLite 部署支持一键恢复"})
		return
	}

	// Serialize against self-update; both mutate files under the install dir.
	if ok := a.selfUpdating.TryLock(); !ok {
		c.JSON(http.StatusConflict, gin.H{"error": "已有维护任务正在进行"})
		return
	}
	defer a.selfUpdating.Unlock()

	// Enforce the upload limit while multipart parsing reads the body. Checking
	// FileHeader.Size alone is too late because ParseMultipartForm may already
	// have spooled an arbitrarily large request to disk.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBackupRequest)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "备份文件过大"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少上传文件（字段名 file）"})
		return
	}
	if fileHeader.Size < 0 || fileHeader.Size > maxBackupUpload {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "备份文件过大"})
		return
	}
	src, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取上传失败：" + err.Error()})
		return
	}
	defer src.Close()

	// Stage the extracted DB + secret next to the live DB so the final rename is
	// on the same filesystem (atomic) and confined to our writable install dir.
	stageDir, err := os.MkdirTemp(filepath.Dir(dbPath), ".restore-")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建暂存目录失败：" + err.Error()})
		return
	}
	defer os.RemoveAll(stageDir)

	stagedDB, importedSecret, err := extractBackup(src, stageDir)
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
			_ = copyFile(rollback, dbPath)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "写入恢复数据库失败：" + err2.Error()})
			a.scheduleRestart()
			return
		}
	}

	// 4) Persist the imported jwt_secret so admin/user sessions and, more
	// importantly, nothing about agent auth changes across the migration.
	secretApplied, restoreWarning := persistRestoredJWTSecret(a.cfgPath, dbPath, importedSecret)
	if restoreWarning != "" {
		c.Header("X-Restore-Warning", restoreWarning)
	}
	if err := pruneRestoreRollbacks(dbPath, maxRestoreRollbacks); err != nil {
		log.Printf("prune pre-restore database snapshots: %v", err)
	}

	msg := "恢复完成，面板即将重启以加载导入的数据。"
	if restoreWarning != "" {
		msg = "数据已恢复，但 jwt_secret 未能自动应用：" + restoreWarning
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
	a.scheduleRestart()
}

// scheduleRestart exits the process shortly after, giving the HTTP response time
// to flush. A supervisor (systemd Restart=always, Docker restart policy) brings
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
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", "", fmt.Errorf("不是有效的 gzip 文件")
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
	for {
		hdr, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", "", e
		}
		entries++
		if entries > maxEntries {
			return "", "", fmt.Errorf("备份归档条目过多（> %d）", maxEntries)
		}
		if hdr.Size < 0 || hdr.Size > maxBackupPayload-payloadSize {
			return "", "", fmt.Errorf("备份归档解压后内容过大")
		}
		payloadSize += hdr.Size
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Base(hdr.Name) // strip any directory / traversal
		switch name {
		case "singbox-panel.db":
			if seenDB {
				return "", "", fmt.Errorf("备份中包含重复的 singbox-panel.db")
			}
			seenDB = true
			if hdr.Size > maxBackupDatabase {
				return "", "", fmt.Errorf("备份中的数据库文件过大")
			}
			dst := filepath.Join(stageDir, "singbox-panel.db")
			f, e := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if e != nil {
				return "", "", e
			}
			if _, e := io.CopyN(f, tr, hdr.Size); e != nil {
				_ = f.Close()
				return "", "", e
			}
			if e := f.Close(); e != nil {
				return "", "", e
			}
			dbPath = dst
		case "jwt_secret":
			if seenSecret {
				return "", "", fmt.Errorf("备份中包含重复的 jwt_secret")
			}
			seenSecret = true
			if hdr.Size > maxBackupSecret {
				return "", "", fmt.Errorf("备份中的 jwt_secret 过大")
			}
			b := make([]byte, int(hdr.Size))
			_, e := io.ReadFull(tr, b)
			if e != nil {
				return "", "", e
			}
			secret = strings.TrimSpace(string(b))
		}
	}
	// tar.Reader stops at the end-of-archive marker. Drain the gzip stream so a
	// truncated checksum or an oversized decompressed tail is not silently
	// accepted.
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return "", "", err
	}
	if limited.N <= 0 {
		return "", "", fmt.Errorf("备份归档解压后内容过大")
	}
	return dbPath, secret, nil
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
	line := "jwt_secret: " + strconv.Quote(secret)
	var out string
	if jwtSecretLine.Match(raw) {
		out = jwtSecretLine.ReplaceAllStringFunc(string(raw), func(string) string { return line })
	} else {
		out = strings.TrimRight(string(raw), "\n") + "\n" + line + "\n"
	}
	return writeFileAtomic(cfgPath, []byte(out), 0o600)
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
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
