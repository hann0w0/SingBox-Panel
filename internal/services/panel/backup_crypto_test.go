package panel

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/config"
)

const testBackupPassphrase = "correct horse battery staple"

func init() {
	// Production parameters cost ~64 MiB per derivation; keep tests fast. The
	// parameters travel in the header, so decryption still exercises parsing.
	defaultBackupKDF = backupKDFParams{Time: 1, Memory: minBackupKDFMemory, Threads: 1}
}

func encryptForTest(t *testing.T, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := encryptBackup(&out, bytes.NewReader(plain), testBackupPassphrase); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func decryptForTest(sealed []byte, passphrase string) ([]byte, error) {
	r, _, err := backupPlainReader(bytes.NewReader(sealed), passphrase)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func TestBackupEncryptionRoundTrip(t *testing.T) {
	for _, size := range []int{0, 1, backupChunkSize - 1, backupChunkSize, backupChunkSize + 1, 3*backupChunkSize + 17} {
		plain := make([]byte, size)
		_, _ = rand.Read(plain)
		sealed := encryptForTest(t, plain)
		if bytes.Contains(sealed, plain[:min(size, 64)]) && size >= 16 {
			t.Fatalf("size %d: ciphertext contains plaintext", size)
		}
		got, err := decryptForTest(sealed, testBackupPassphrase)
		if err != nil {
			t.Fatalf("size %d: decrypt: %v", size, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size %d: round trip mismatch", size)
		}
	}
}

func TestBackupDecryptionRejectsWrongPassphraseAndTampering(t *testing.T) {
	plain := bytes.Repeat([]byte("node-secret "), 3*backupChunkSize/12)
	sealed := encryptForTest(t, plain)

	if _, err := decryptForTest(sealed, "wrong passphrase!!"); err == nil {
		t.Fatal("wrong passphrase decrypted")
	}

	chunk := backupChunkSize + backupTagSize
	cases := map[string][]byte{
		// Dropping whole trailing chunks leaves a stream without a final chunk.
		"truncated at chunk boundary": sealed[:backupHeaderSize+chunk],
		"truncated mid chunk":         sealed[:len(sealed)-5],
		"swapped chunks": func() []byte {
			b := append([]byte(nil), sealed...)
			first := append([]byte(nil), b[backupHeaderSize:backupHeaderSize+chunk]...)
			copy(b[backupHeaderSize:], b[backupHeaderSize+chunk:backupHeaderSize+2*chunk])
			copy(b[backupHeaderSize+chunk:], first)
			return b
		}(),
		"flipped body bit": func() []byte {
			b := append([]byte(nil), sealed...)
			b[backupHeaderSize+10] ^= 1
			return b
		}(),
		"flipped header salt": func() []byte {
			b := append([]byte(nil), sealed...)
			b[len(backupCryptoMagic)] ^= 1
			return b
		}(),
		"appended data": append(append([]byte(nil), sealed...), 0),
	}
	for name, data := range cases {
		if _, err := decryptForTest(data, testBackupPassphrase); err == nil {
			t.Fatalf("%s: tampered backup decrypted", name)
		}
	}
}

func TestBackupDecryptionRejectsHostileKDFParameters(t *testing.T) {
	sealed := encryptForTest(t, []byte("archive"))
	memoryAt := len(backupCryptoMagic) + backupSaltSize + 4
	b := append([]byte(nil), sealed...)
	b[memoryAt], b[memoryAt+1], b[memoryAt+2], b[memoryAt+3] = 0xff, 0xff, 0xff, 0xff
	if _, _, err := backupPlainReader(bytes.NewReader(b), testBackupPassphrase); err == nil || !strings.Contains(err.Error(), "参数无效") {
		t.Fatalf("hostile memory parameter err = %v", err)
	}
}

func TestBackupPlainReaderPassesPlaintextAndRequiresPassphrase(t *testing.T) {
	r, encrypted, err := backupPlainReader(strings.NewReader("\x1f\x8bplain"), "")
	if err != nil || encrypted {
		t.Fatalf("plaintext: encrypted = %v, err = %v", encrypted, err)
	}
	if got, _ := io.ReadAll(r); string(got) != "\x1f\x8bplain" {
		t.Fatalf("plaintext passthrough = %q", got)
	}
	sealed := encryptForTest(t, []byte("archive"))
	if _, encrypted, err := backupPlainReader(bytes.NewReader(sealed), ""); !encrypted || !errors.Is(err, errBackupPassphraseRequired) {
		t.Fatalf("missing passphrase: encrypted = %v, err = %v", encrypted, err)
	}
}

func TestValidateBackupPassphrase(t *testing.T) {
	if validateBackupPassphrase("short") == nil {
		t.Fatal("short passphrase accepted")
	}
	if validateBackupPassphrase(strings.Repeat("a", maxBackupPassphraseLen+1)) == nil {
		t.Fatal("oversized passphrase accepted")
	}
	if err := validateBackupPassphrase("十二个中文字符组成的备份口令啊"); err != nil {
		t.Fatalf("multibyte passphrase rejected: %v", err)
	}
}

func TestOneDriveSyncRefusesPlaintextUpload(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	a := &App{
		cfg: config.PanelConfig{
			JWTSecret: testLiveJWTSecret,
			Database:  config.DatabaseConfig{Driver: "sqlite", DSN: dbPath},
		},
		db: testDBAt(t, dbPath),
	}
	if err := a.saveOneDriveSettings(oneDriveSettings{RefreshToken: "refresh-token", AutoSync: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.syncOneDriveBackupLocked(); err == nil || !strings.Contains(err.Error(), "加密口令") {
		t.Fatalf("sync without passphrase err = %v", err)
	}
	settings, err := a.loadOneDriveSettings()
	if err != nil || settings.FailedAttempts != 1 || !strings.Contains(settings.LastError, "加密口令") {
		t.Fatalf("failure not recorded: %#v, err = %v", settings, err)
	}
}

func TestOneDrivePassphraseIsStoredEncrypted(t *testing.T) {
	a := &App{cfg: config.PanelConfig{JWTSecret: testLiveJWTSecret}, db: testDB(t)}
	router := gin.New()
	router.PUT("/passphrase", a.setOneDrivePassphrase)

	body := `{"passphrase":"` + testBackupPassphrase + `"}`
	req := httptest.NewRequest(http.MethodPut, "/passphrase", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var raw struct{ Value string }
	if err := a.db.Table("settings").Where("key = ?", oneDriveSettingKey).Select("value").Scan(&raw).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw.Value, testBackupPassphrase) {
		t.Fatalf("passphrase stored in plaintext: %s", raw.Value)
	}
	var stored oneDriveStoredSettings
	if err := json.Unmarshal([]byte(raw.Value), &stored); err != nil || !strings.HasPrefix(stored.EncryptedPassphrase, "v1:") {
		t.Fatalf("stored = %#v, err = %v", stored, err)
	}
	settings, err := a.loadOneDriveSettings()
	if err != nil || settings.Passphrase != testBackupPassphrase {
		t.Fatalf("loaded passphrase = %q, err = %v", settings.Passphrase, err)
	}

	short := httptest.NewRequest(http.MethodPut, "/passphrase", strings.NewReader(`{"passphrase":"short"}`))
	short.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, short)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("short passphrase status = %d", rec.Code)
	}
}

func TestOneDriveSyncRemovesLegacyPlaintextBackup(t *testing.T) {
	oldOAuthURL, oldGraphURL := oneDriveOAuthBaseURL, oneDriveGraphBaseURL
	defer func() {
		oneDriveOAuthBaseURL = oldOAuthURL
		oneDriveGraphBaseURL = oldGraphURL
	}()

	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			_ = json.NewEncoder(w).Encode(oneDriveTokenResponse{AccessToken: "access-token", RefreshToken: "refresh-token"})
		case r.URL.Path == "/me/drive/root:/singbox-panel-backups:":
			_ = json.NewEncoder(w).Encode(oneDriveFile{ID: "folder-id", Name: oneDriveBackupFolder, Folder: &struct{}{}})
		case r.URL.Path == "/me/drive/items/folder-id:/"+oneDriveBackupName+":/createUploadSession":
			_ = json.NewEncoder(w).Encode(map[string]string{"uploadUrl": serverURL(r) + "/upload"})
		case r.URL.Path == "/upload":
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"encrypted-id"}`))
		case r.URL.Path == "/me/drive/items/folder-id/children":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{
				{"id": "encrypted-id", "name": oneDriveBackupName, "file": map[string]any{}},
				{"id": "legacy-id", "name": oneDriveLegacyBackupName, "file": map[string]any{}},
			}})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/me/drive/items/"):
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/me/drive/items/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	oneDriveOAuthBaseURL = server.URL
	oneDriveGraphBaseURL = server.URL

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "singbox-panel.db")
	a := &App{
		cfg: config.PanelConfig{
			JWTSecret: testLiveJWTSecret,
			Database:  config.DatabaseConfig{Driver: "sqlite", DSN: dbPath},
		},
		db: testDBAt(t, dbPath),
	}
	if err := a.saveOneDriveSettings(oneDriveSettings{RefreshToken: "refresh-token", Passphrase: testBackupPassphrase}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.syncOneDriveBackupLocked(); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "legacy-id" {
		t.Fatalf("deleted = %v, want only the legacy plaintext backup", deleted)
	}
}
