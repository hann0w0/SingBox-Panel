package panel

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
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
//  3. persist jwt_secret into panel.yaml so sessions/agents survive,
//  4. tell the client to expect a restart, then exit so systemd restarts us
//     cleanly against the restored files.

const (
	maxBackupUpload = 512 << 20 // 512 MiB ceiling on the uploaded archive
	sqliteMagic     = "SQLite format 3\x00"
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

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少上传文件（字段名 file）"})
		return
	}
	if fileHeader.Size > maxBackupUpload {
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
	rollback := fmt.Sprintf("%s.pre-restore-%s", dbPath, time.Now().Format("20060102-150405"))
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
	secretApplied := false
	if importedSecret != "" && a.cfgPath != "" {
		if err := rewriteJWTSecret(a.cfgPath, importedSecret); err != nil {
			// Non-fatal: the DB is already restored. Warn but continue; the
			// operator can set jwt_secret by hand if needed.
			c.Header("X-Restore-Warning", "jwt_secret 未能写入配置："+err.Error())
		} else {
			secretApplied = true
		}
	}

	msg := "恢复完成，面板即将重启以加载导入的数据。"
	if importedSecret != "" && !secretApplied {
		msg = "数据已恢复，但 jwt_secret 未写入配置（可能是 Docker 部署）。请手动设置后重启，否则登录会话会失效。"
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
	tr := tar.NewReader(gz)
	// Defensive cap on archive entries: only the two known files are ever
	// written, but a pathological archive with a huge number of headers would
	// otherwise force an unbounded stream scan.
	const maxEntries = 10_000
	entries := 0
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
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Base(hdr.Name) // strip any directory / traversal
		switch name {
		case "singbox-panel.db":
			dst := filepath.Join(stageDir, "singbox-panel.db")
			f, e := os.Create(dst)
			if e != nil {
				return "", "", e
			}
			// Cap extraction to the same ceiling as the upload.
			if _, e := io.Copy(f, io.LimitReader(tr, maxBackupUpload)); e != nil {
				f.Close()
				return "", "", e
			}
			f.Close()
			dbPath = dst
		case "jwt_secret":
			b, e := io.ReadAll(io.LimitReader(tr, 4096))
			if e != nil {
				return "", "", e
			}
			secret = strings.TrimSpace(string(b))
		}
	}
	return dbPath, secret, nil
}

// validateSQLiteFile confirms the staged file is really an SQLite database, so
// a mistaken upload cannot clobber the live DB with garbage.
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
	return nil
}

var jwtSecretLine = regexp.MustCompile(`(?m)^jwt_secret:.*$`)

// rewriteJWTSecret replaces (or appends) the jwt_secret line in panel.yaml,
// preserving the rest of the file. The value is always double-quoted; a hex
// secret needs no escaping, but guard against stray quotes just in case.
func rewriteJWTSecret(cfgPath, secret string) error {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	safe := strings.ReplaceAll(secret, `"`, "")
	line := fmt.Sprintf(`jwt_secret: "%s"`, safe)
	var out string
	if jwtSecretLine.Match(raw) {
		out = jwtSecretLine.ReplaceAllString(string(raw), line)
	} else {
		out = strings.TrimRight(string(raw), "\n") + "\n" + line + "\n"
	}
	// Preserve 0600 on the config which holds credentials.
	return os.WriteFile(cfgPath, []byte(out), 0o600)
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
