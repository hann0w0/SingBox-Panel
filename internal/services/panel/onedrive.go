package panel

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

const (
	oneDriveSettingKey    = "onedrive_backup"
	oneDriveBackupFolder  = "singbox-panel-backups"
	oneDriveBackupName    = "singbox-panel-backup.tar.gz"
	oneDriveScope         = "offline_access Files.ReadWrite"
	oneDriveSyncInterval  = 1 * time.Hour
	oneDriveRetryBase     = 15 * time.Minute
	oneDriveRetryMax      = 6 * time.Hour
	oneDriveOAuthClientID = "d50ca740-c83f-4d1b-b616-12c519384f0c"
	oneDriveMaxListPages  = 100
	oneDriveMaxJSONBody   = 4 << 20
	oneDriveListTimeout   = 45 * time.Second
	oneDriveStreamTimeout = 30 * time.Minute
)

// These variables are replaceable in tests without changing the production
// endpoints.
var (
	oneDriveOAuthBaseURL      = "https://login.microsoftonline.com/common/oauth2/v2.0"
	oneDriveGraphBaseURL      = "https://graph.microsoft.com/v1.0"
	errOneDriveBackupNotFound = errors.New("OneDrive 备份不存在")
)

type oneDriveStoredSettings struct {
	ClientID         string     `json:"client_id"`
	EncryptedRefresh string     `json:"refresh_token"`
	AutoSync         bool       `json:"auto_sync"`
	LastSyncAt       *time.Time `json:"last_sync_at,omitempty"`
	LastAttemptAt    *time.Time `json:"last_attempt_at,omitempty"`
	FailedAttempts   int        `json:"failed_attempts,omitempty"`
	LastBackupName   string     `json:"last_backup_name,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
}

type oneDriveSettings struct {
	ClientID       string
	RefreshToken   string
	AutoSync       bool
	LastSyncAt     *time.Time
	LastAttemptAt  *time.Time
	FailedAttempts int
	LastBackupName string
	LastError      string
}

type oneDriveDeviceSession struct {
	ClientID        string
	DeviceCode      string
	IntervalSeconds int
	ExpiresAt       time.Time
}

type oneDriveDeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	Message                 string `json:"message"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type oneDriveTokenResponse struct {
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

type oneDriveFile struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	Size                 int64     `json:"size"`
	LastModifiedDateTime string    `json:"lastModifiedDateTime"`
	Folder               *struct{} `json:"folder"`
	File                 *struct {
		MimeType string `json:"mimeType"`
	} `json:"file"`
}

type oneDriveFilePage struct {
	Value    []oneDriveFile `json:"value"`
	NextLink string         `json:"@odata.nextLink"`
}

func defaultOneDriveSettings() oneDriveSettings {
	return oneDriveSettings{ClientID: oneDriveOAuthClientID}
}

func (a *App) loadOneDriveSettings() (oneDriveSettings, error) {
	settings := defaultOneDriveSettings()
	var row model.Setting
	result := a.db.Where("key = ?", oneDriveSettingKey).Limit(1).Find(&row)
	if result.Error != nil {
		return settings, result.Error
	}
	if result.RowsAffected == 0 || strings.TrimSpace(row.Value) == "" {
		return settings, nil
	}
	var stored oneDriveStoredSettings
	if err := json.Unmarshal([]byte(row.Value), &stored); err != nil {
		return settings, fmt.Errorf("读取 OneDrive 配置失败：%w", err)
	}
	settings.AutoSync = stored.AutoSync
	settings.LastSyncAt = stored.LastSyncAt
	settings.LastAttemptAt = stored.LastAttemptAt
	settings.FailedAttempts = stored.FailedAttempts
	settings.LastBackupName = stored.LastBackupName
	settings.LastError = stored.LastError
	if stored.EncryptedRefresh != "" {
		refresh, decryptErr := a.decryptOneDriveSecret(stored.EncryptedRefresh)
		if decryptErr != nil {
			return settings, fmt.Errorf("读取 OneDrive 授权失败：%w", decryptErr)
		}
		settings.RefreshToken = refresh
		if clientID := strings.TrimSpace(stored.ClientID); clientID != "" {
			// Existing authorizations remain bound to the client that issued them.
			settings.ClientID = clientID
		}
	}
	return settings, nil
}

func (a *App) saveOneDriveSettings(settings oneDriveSettings) error {
	if settings.ClientID == "" {
		settings.ClientID = oneDriveOAuthClientID
	}
	refresh := ""
	if settings.RefreshToken != "" {
		var err error
		refresh, err = a.encryptOneDriveSecret(settings.RefreshToken)
		if err != nil {
			return err
		}
	}
	value, err := json.Marshal(oneDriveStoredSettings{
		ClientID:         strings.TrimSpace(settings.ClientID),
		EncryptedRefresh: refresh,
		AutoSync:         settings.AutoSync,
		LastSyncAt:       settings.LastSyncAt,
		LastAttemptAt:    settings.LastAttemptAt,
		FailedAttempts:   settings.FailedAttempts,
		LastBackupName:   settings.LastBackupName,
		LastError:        settings.LastError,
	})
	if err != nil {
		return fmt.Errorf("序列化 OneDrive 配置失败：%w", err)
	}
	return a.db.Save(&model.Setting{Key: oneDriveSettingKey, Value: string(value)}).Error
}

func (a *App) mutateOneDriveSettings(update func(*oneDriveSettings) error) error {
	a.oneDriveSettingsMu.Lock()
	defer a.oneDriveSettingsMu.Unlock()

	settings, err := a.loadOneDriveSettings()
	if err != nil {
		return err
	}
	if err := update(&settings); err != nil {
		return err
	}
	return a.saveOneDriveSettings(settings)
}

func (a *App) oneDriveSecretKey() []byte {
	key := sha256.Sum256([]byte("singbox-panel/onedrive/" + a.cfg.JWTSecret))
	return key[:]
}

func (a *App) encryptOneDriveSecret(value string) (string, error) {
	block, err := aes.NewCipher(a.oneDriveSecretKey())
	if err != nil {
		return "", fmt.Errorf("创建 OneDrive 加密器失败：%w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("创建 OneDrive 加密器失败：%w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("生成 OneDrive 加密随机数失败：%w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(value), nil)
	return "v1:" + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (a *App) decryptOneDriveSecret(value string) (string, error) {
	if !strings.HasPrefix(value, "v1:") {
		return value, nil
	}
	encoded := strings.TrimPrefix(value, "v1:")
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", errors.New("OneDrive 授权数据损坏")
	}
	block, err := aes.NewCipher(a.oneDriveSecretKey())
	if err != nil {
		return "", fmt.Errorf("创建 OneDrive 解密器失败：%w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(sealed) < gcm.NonceSize() {
		return "", errors.New("OneDrive 授权数据损坏")
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", errors.New("无法解密 OneDrive 授权，请重新连接")
	}
	return string(plain), nil
}

func oneDriveHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

func oneDriveStreamingHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	// Unlike the small JSON client, this client must permit large archives, but
	// it still needs an overall deadline so a stalled response body cannot hold
	// the global maintenance lock forever.
	return &http.Client{Transport: transport, Timeout: oneDriveStreamTimeout}
}

func readOneDriveError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		ErrorDesc string `json:"error_description"`
	}
	if json.Unmarshal(body, &payload) == nil {
		msg := payload.Error.Message
		if msg == "" {
			msg = payload.ErrorDesc
		}
		if msg != "" {
			if payload.Error.Code != "" {
				return fmt.Errorf("OneDrive 请求失败（HTTP %d，%s）：%s", resp.StatusCode, payload.Error.Code, msg)
			}
			return fmt.Errorf("OneDrive 请求失败（HTTP %d）：%s", resp.StatusCode, msg)
		}
	}
	return fmt.Errorf("OneDrive 请求失败（HTTP %d）", resp.StatusCode)
}

func decodeOneDriveJSON(r io.Reader, target any) error {
	body, err := io.ReadAll(io.LimitReader(r, oneDriveMaxJSONBody+1))
	if err != nil {
		return err
	}
	if len(body) > oneDriveMaxJSONBody {
		return fmt.Errorf("OneDrive JSON 响应超过 %d 字节限制", oneDriveMaxJSONBody)
	}
	return json.Unmarshal(body, target)
}

func oneDriveTokenRequestContext(ctx context.Context, values url.Values) (oneDriveTokenResponse, error) {
	var token oneDriveTokenResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oneDriveOAuthBaseURL+"/token", strings.NewReader(values.Encode()))
	if err != nil {
		return token, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := oneDriveHTTPClient().Do(req)
	if err != nil {
		return token, fmt.Errorf("连接 OneDrive 授权服务失败：%w", err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return token, fmt.Errorf("读取 OneDrive 授权响应失败：%w", readErr)
	}
	_ = json.Unmarshal(body, &token)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if token.Error != "" && token.ErrorDesc != "" {
			return token, fmt.Errorf("OneDrive 授权失败 [%s]：%s", token.Error, token.ErrorDesc)
		}
		if token.ErrorDesc != "" {
			return token, fmt.Errorf("OneDrive 授权失败：%s", token.ErrorDesc)
		}
		return token, fmt.Errorf("OneDrive 授权失败（HTTP %d）", resp.StatusCode)
	}
	if token.AccessToken == "" {
		return token, errors.New("OneDrive 授权响应缺少 access_token")
	}
	return token, nil
}

func (a *App) startOneDriveDeviceCode(ctx context.Context, clientID string) (oneDriveDeviceCodeResponse, error) {
	var result oneDriveDeviceCodeResponse
	values := url.Values{"client_id": {clientID}, "scope": {oneDriveScope}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oneDriveOAuthBaseURL+"/devicecode", strings.NewReader(values.Encode()))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := oneDriveHTTPClient().Do(req)
	if err != nil {
		return result, fmt.Errorf("连接 OneDrive 授权服务失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, readOneDriveError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return result, fmt.Errorf("解析 OneDrive 授权响应失败：%w", err)
	}
	if result.DeviceCode == "" || result.UserCode == "" || result.VerificationURI == "" {
		return result, errors.New("OneDrive 授权响应不完整")
	}
	if result.Interval <= 0 {
		result.Interval = 5
	}
	return result, nil
}

// GET /api/admin/maintenance/onedrive — status and data-only cloud backups.
func (a *App) oneDriveStatus(c *gin.Context) {
	unlockMaintenance, ok := a.tryMaintenanceRequest(c)
	if !ok {
		return
	}
	defer unlockMaintenance()

	settings, err := a.loadOneDriveSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	resp := gin.H{
		"connected":        settings.RefreshToken != "",
		"auto_sync":        settings.AutoSync,
		"interval_hours":   int(oneDriveSyncInterval / time.Hour),
		"last_sync_at":     settings.LastSyncAt,
		"last_backup_name": settings.LastBackupName,
		"last_error":       settings.LastError,
		"folder":           oneDriveBackupFolder,
		"backup_name":      oneDriveBackupName,
		"files":            []oneDriveFile{},
	}
	if !resp["connected"].(bool) {
		c.JSON(http.StatusOK, resp)
		return
	}
	files, listErr := a.listOneDriveBackupsContext(c.Request.Context())
	if listErr != nil {
		resp["cloud_error"] = listErr.Error()
	} else {
		resp["files"] = files
	}
	c.JSON(http.StatusOK, resp)
}

// POST /api/admin/maintenance/onedrive/auth/start — begin device-code login.
func (a *App) startOneDriveAuth(c *gin.Context) {
	unlockMaintenance, ok := a.tryMaintenanceRequest(c)
	if !ok {
		return
	}
	defer unlockMaintenance()

	a.pruneOneDrivePending(time.Now())
	device, err := a.startOneDriveDeviceCode(c.Request.Context(), oneDriveOAuthClientID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	sessionID := uuid.NewString()
	a.oneDriveMu.Lock()
	if a.oneDrivePending == nil {
		a.oneDrivePending = make(map[string]oneDriveDeviceSession)
	}
	a.oneDrivePending[sessionID] = oneDriveDeviceSession{
		ClientID:        oneDriveOAuthClientID,
		DeviceCode:      device.DeviceCode,
		IntervalSeconds: device.Interval,
		ExpiresAt:       time.Now().Add(time.Duration(device.ExpiresIn) * time.Second),
	}
	a.oneDriveMu.Unlock()
	c.JSON(http.StatusOK, gin.H{
		"session_id":                sessionID,
		"user_code":                 device.UserCode,
		"verification_uri":          device.VerificationURI,
		"verification_uri_complete": device.VerificationURIComplete,
		"message":                   device.Message,
		"interval":                  device.Interval,
		"expires_in":                device.ExpiresIn,
	})
}

// POST /api/admin/maintenance/onedrive/auth/:sessionID/poll — poll device login.
func (a *App) pollOneDriveAuth(c *gin.Context) {
	unlockMaintenance, ok := a.tryMaintenanceRequest(c)
	if !ok {
		return
	}
	defer unlockMaintenance()

	sessionID := c.Param("sessionID")
	a.oneDriveMu.Lock()
	session, ok := a.oneDrivePending[sessionID]
	a.oneDriveMu.Unlock()
	if !ok {
		c.JSON(http.StatusGone, gin.H{"error": "OneDrive 授权已过期，请重新开始连接"})
		return
	}
	if time.Now().After(session.ExpiresAt) {
		a.oneDriveMu.Lock()
		delete(a.oneDrivePending, sessionID)
		a.oneDriveMu.Unlock()
		c.JSON(http.StatusGone, gin.H{"error": "OneDrive 授权已过期，请重新开始连接"})
		return
	}
	values := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {session.ClientID},
		"device_code": {session.DeviceCode},
	}
	token, err := oneDriveTokenRequestContext(c.Request.Context(), values)
	if err != nil {
		if strings.Contains(err.Error(), "authorization_pending") || strings.Contains(err.Error(), "授权请求待处理") {
			c.JSON(http.StatusAccepted, gin.H{"status": "pending"})
			return
		}
		if strings.Contains(err.Error(), "slow_down") {
			a.oneDriveMu.Lock()
			session.IntervalSeconds += 5
			a.oneDrivePending[sessionID] = session
			a.oneDriveMu.Unlock()
			c.JSON(http.StatusAccepted, gin.H{"status": "pending", "interval": session.IntervalSeconds})
			return
		}
		a.oneDriveMu.Lock()
		delete(a.oneDrivePending, sessionID)
		a.oneDriveMu.Unlock()
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if token.RefreshToken == "" {
		a.oneDriveMu.Lock()
		delete(a.oneDrivePending, sessionID)
		a.oneDriveMu.Unlock()
		c.JSON(http.StatusBadGateway, gin.H{"error": "OneDrive 授权响应缺少 refresh_token，请重新连接"})
		return
	}

	err = a.mutateOneDriveSettings(func(settings *oneDriveSettings) error {
		settings.ClientID = session.ClientID
		settings.RefreshToken = token.RefreshToken
		settings.AutoSync = true
		settings.LastAttemptAt = nil
		settings.FailedAttempts = 0
		settings.LastError = ""
		return nil
	})
	a.oneDriveMu.Lock()
	delete(a.oneDrivePending, sessionID)
	a.oneDriveMu.Unlock()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "connected"})
}

func (a *App) accessTokenOneDriveContext(ctx context.Context) (string, error) {
	a.oneDriveTokenMu.Lock()
	defer a.oneDriveTokenMu.Unlock()

	settings, err := a.loadOneDriveSettings()
	if err != nil {
		return "", err
	}
	if settings.RefreshToken == "" {
		return "", errors.New("尚未连接 OneDrive")
	}
	if settings.ClientID == "" {
		settings.ClientID = oneDriveOAuthClientID
	}
	token, err := oneDriveTokenRequestContext(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {settings.ClientID},
		"refresh_token": {settings.RefreshToken},
		"scope":         {oneDriveScope},
	})
	if err != nil {
		return "", err
	}
	if err := a.mutateOneDriveSettings(func(current *oneDriveSettings) error {
		if current.ClientID != settings.ClientID || current.RefreshToken != settings.RefreshToken {
			return errors.New("OneDrive 授权已更新，请重试当前操作")
		}
		if token.RefreshToken != "" {
			current.RefreshToken = token.RefreshToken
		}
		return nil
	}); err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

func oneDriveRequest(ctx context.Context, method, endpoint, accessToken string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return oneDriveHTTPClient().Do(req)
}

func oneDriveGraphJSON(ctx context.Context, method, endpoint, accessToken string, body any, target any) error {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	resp, err := oneDriveRequest(ctx, method, endpoint, accessToken, raw)
	if err != nil {
		return fmt.Errorf("连接 OneDrive 图形 API 失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return readOneDriveError(resp)
	}
	if target == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := decodeOneDriveJSON(resp.Body, target); err != nil {
		return fmt.Errorf("解析 OneDrive 响应失败：%w", err)
	}
	return nil
}

func validateOneDriveNextLink(next string) (string, error) {
	if strings.TrimSpace(next) == "" {
		return "", nil
	}
	base, err := url.Parse(oneDriveGraphBaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", errors.New("OneDrive 图形 API 基础地址无效")
	}
	candidate, err := url.Parse(next)
	if err != nil || candidate.Scheme == "" || candidate.Host == "" {
		return "", errors.New("OneDrive 返回了无效的分页地址")
	}
	if !strings.EqualFold(candidate.Scheme, base.Scheme) || !strings.EqualFold(candidate.Host, base.Host) {
		return "", errors.New("OneDrive 返回了不受信任的分页地址")
	}
	basePath := strings.TrimSuffix(base.EscapedPath(), "/")
	path := candidate.EscapedPath()
	if basePath != "" && basePath != "/" && path != basePath && !strings.HasPrefix(path, basePath+"/") {
		return "", errors.New("OneDrive 返回了基础路径之外的分页地址")
	}
	return candidate.String(), nil
}

func (a *App) oneDriveFolderIDContext(ctx context.Context) (string, error) {
	token, err := a.accessTokenOneDriveContext(ctx)
	if err != nil {
		return "", err
	}
	folderEndpoint := oneDriveGraphBaseURL + "/me/drive/root:/" + url.PathEscape(oneDriveBackupFolder) + ":"
	var folder oneDriveFile
	if err := oneDriveGraphJSON(ctx, http.MethodGet, folderEndpoint, token, nil, &folder); err == nil && folder.ID != "" && folder.Folder != nil {
		return folder.ID, nil
	} else if err == nil && folder.ID != "" {
		return "", fmt.Errorf("OneDrive 根目录中的 %s 不是文件夹", oneDriveBackupFolder)
	} else if err != nil && !strings.Contains(err.Error(), "HTTP 404") {
		return "", err
	}

	createEndpoint := oneDriveGraphBaseURL + "/me/drive/root/children"
	var created oneDriveFile
	err = oneDriveGraphJSON(ctx, http.MethodPost, createEndpoint, token, map[string]any{
		"name":                              oneDriveBackupFolder,
		"folder":                            map[string]any{},
		"@microsoft.graph.conflictBehavior": "fail",
	}, &created)
	if err == nil && created.ID != "" && created.Folder != nil {
		return created.ID, nil
	}
	// A concurrent request may have created the folder between GET and POST.
	if err != nil && strings.Contains(err.Error(), "nameAlreadyExists") {
		if getErr := oneDriveGraphJSON(ctx, http.MethodGet, folderEndpoint, token, nil, &folder); getErr == nil && folder.Folder != nil {
			return folder.ID, nil
		}
	}
	if err != nil {
		return "", err
	}
	return "", errors.New("OneDrive 备份目录创建失败")
}

func (a *App) listOneDriveBackupsContext(ctx context.Context) ([]oneDriveFile, error) {
	ctx, cancel := context.WithTimeout(ctx, oneDriveListTimeout)
	defer cancel()
	folderID, err := a.oneDriveFolderIDContext(ctx)
	if err != nil {
		return nil, err
	}
	token, err := a.accessTokenOneDriveContext(ctx)
	if err != nil {
		return nil, err
	}
	next := oneDriveGraphBaseURL + "/me/drive/items/" + url.PathEscape(folderID) + "/children?$select=id,name,size,lastModifiedDateTime,file&$orderby=lastModifiedDateTime%20desc"
	files := make([]oneDriveFile, 0)
	for pageNumber := 0; next != ""; pageNumber++ {
		if pageNumber >= oneDriveMaxListPages {
			return nil, fmt.Errorf("OneDrive 文件列表超过 %d 页，已停止读取", oneDriveMaxListPages)
		}
		var page oneDriveFilePage
		resp, requestErr := oneDriveRequest(ctx, http.MethodGet, next, token, nil)
		if requestErr != nil {
			return nil, fmt.Errorf("连接 OneDrive 图形 API 失败：%w", requestErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			err := readOneDriveError(resp)
			resp.Body.Close()
			return nil, err
		}
		if err := decodeOneDriveJSON(resp.Body, &page); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("解析 OneDrive 文件列表失败：%w", err)
		}
		resp.Body.Close()
		for _, file := range page.Value {
			if file.File != nil && file.Name == oneDriveBackupName {
				files = append(files, file)
			}
		}
		next, err = validateOneDriveNextLink(page.NextLink)
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

func (a *App) listOneDriveBackups() ([]oneDriveFile, error) {
	return a.listOneDriveBackupsContext(context.Background())
}

func (a *App) oneDriveBackupByIDContext(ctx context.Context, id string) (oneDriveFile, error) {
	files, err := a.listOneDriveBackupsContext(ctx)
	if err != nil {
		return oneDriveFile{}, err
	}
	for _, file := range files {
		if file.ID == id {
			return file, nil
		}
	}
	return oneDriveFile{}, errOneDriveBackupNotFound
}

func (a *App) oneDriveBackupByID(id string) (oneDriveFile, error) {
	return a.oneDriveBackupByIDContext(context.Background(), id)
}

func (a *App) uploadOneDriveBackupContext(ctx context.Context, archivePath string) error {
	ctx, cancel := context.WithTimeout(ctx, oneDriveStreamTimeout)
	defer cancel()
	folderID, err := a.oneDriveFolderIDContext(ctx)
	if err != nil {
		return err
	}
	token, err := a.accessTokenOneDriveContext(ctx)
	if err != nil {
		return err
	}
	createEndpoint := oneDriveGraphBaseURL + "/me/drive/items/" + url.PathEscape(folderID) + ":/" + url.PathEscape(oneDriveBackupName) + ":/createUploadSession"
	var session struct {
		UploadURL string `json:"uploadUrl"`
	}
	err = oneDriveGraphJSON(ctx, http.MethodPost, createEndpoint, token, map[string]any{
		"item": map[string]any{
			"@microsoft.graph.conflictBehavior": "replace",
			"name":                              oneDriveBackupName,
		},
	}, &session)
	if err != nil {
		return err
	}
	if session.UploadURL == "" {
		return errors.New("OneDrive 未返回上传地址")
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	total := info.Size()
	if total <= 0 {
		return errors.New("OneDrive 备份归档为空")
	}
	const chunkSize int64 = 10 * 320 * 1024
	buf := make([]byte, chunkSize)
	var offset int64
	for offset < total {
		read, readErr := io.ReadFull(f, buf)
		if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
			return fmt.Errorf("读取备份归档失败：%w", readErr)
		}
		if read == 0 {
			break
		}
		end := offset + int64(read) - 1
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, session.UploadURL, bytes.NewReader(buf[:read]))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Length", strconv.Itoa(read))
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, end, total))
		resp, err := (&http.Client{Timeout: 15 * time.Minute}).Do(req)
		if err != nil {
			return fmt.Errorf("上传 OneDrive 备份失败：%w", err)
		}
		finalChunk := end+1 == total
		expectedStatus := resp.StatusCode == http.StatusAccepted
		if finalChunk {
			expectedStatus = resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated
		}
		if !expectedStatus {
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				resp.Body.Close()
				return fmt.Errorf("OneDrive 上传会话返回了意外状态（HTTP %d）", resp.StatusCode)
			}
			uploadErr := readOneDriveError(resp)
			resp.Body.Close()
			return uploadErr
		}
		resp.Body.Close()
		offset += int64(read)
	}
	if offset != total {
		return fmt.Errorf("OneDrive 备份上传不完整：已上传 %d 字节，共 %d 字节", offset, total)
	}
	return nil
}

func (a *App) uploadOneDriveBackup(archivePath string) error {
	return a.uploadOneDriveBackupContext(context.Background(), archivePath)
}

// POST /api/admin/maintenance/onedrive/sync — create and upload one data-only archive.
func (a *App) syncOneDriveBackup(c *gin.Context) {
	if !a.selfUpdating.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "已有维护任务正在进行"})
		return
	}
	releaseMaintenanceLock := true
	defer func() {
		if releaseMaintenanceLock {
			a.selfUpdating.Unlock()
		}
	}()
	if !a.oneDriveSyncMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "已有 OneDrive 备份任务正在进行"})
		return
	}
	defer a.oneDriveSyncMu.Unlock()
	name, err := a.syncOneDriveBackupLockedContext(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "name": name, "message": "备份已同步到 OneDrive"})
}

func (a *App) syncOneDriveBackupLockedContext(ctx context.Context) (string, error) {
	settings, err := a.loadOneDriveSettings()
	if err != nil {
		return "", err
	}
	if settings.RefreshToken == "" {
		return "", errors.New("请先连接 OneDrive")
	}
	attemptedAt := time.Now()
	archivePath, _, cleanup, err := a.createBackupArchive()
	if err != nil {
		a.recordOneDriveSyncFailure(attemptedAt, err)
		return "", err
	}
	defer cleanup()
	if err := a.uploadOneDriveBackupContext(ctx, archivePath); err != nil {
		a.recordOneDriveSyncFailure(attemptedAt, err)
		return "", err
	}
	now := time.Now()
	if err := a.mutateOneDriveSettings(func(current *oneDriveSettings) error {
		current.LastSyncAt = &now
		current.LastAttemptAt = &now
		current.FailedAttempts = 0
		current.LastBackupName = oneDriveBackupName
		current.LastError = ""
		return nil
	}); err != nil {
		return "", err
	}
	return oneDriveBackupName, nil
}

func (a *App) syncOneDriveBackupLocked() (string, error) {
	return a.syncOneDriveBackupLockedContext(context.Background())
}

func (a *App) recordOneDriveSyncFailure(attemptedAt time.Time, syncErr error) {
	if err := a.mutateOneDriveSettings(func(settings *oneDriveSettings) error {
		settings.LastAttemptAt = &attemptedAt
		if settings.FailedAttempts < 32 {
			settings.FailedAttempts++
		}
		settings.LastError = syncErr.Error()
		return nil
	}); err != nil {
		log.Printf("保存 OneDrive 失败状态失败: %v", err)
	}
}

func oneDriveRetryDelay(failedAttempts int) time.Duration {
	if failedAttempts <= 0 {
		return 0
	}
	delay := oneDriveRetryBase
	for i := 1; i < failedAttempts && delay < oneDriveRetryMax; i++ {
		delay *= 2
		if delay > oneDriveRetryMax {
			return oneDriveRetryMax
		}
	}
	return delay
}

// GET /api/admin/maintenance/onedrive/backups/:id/download
func (a *App) downloadOneDriveBackup(c *gin.Context) {
	unlockMaintenance, ok := a.tryMaintenanceRequest(c)
	if !ok {
		return
	}
	maintenanceLocked := true
	defer func() {
		if maintenanceLocked {
			unlockMaintenance()
		}
	}()

	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "备份 ID 为空"})
		return
	}
	file, resp, err := a.openOneDriveBackupContent(c.Request.Context(), id)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errOneDriveBackupNotFound) {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	// Token rotation and cloud lookup are complete. The response body is an
	// immutable remote stream, so a slow browser download need not block a later
	// backup or update operation.
	unlockMaintenance()
	maintenanceLocked = false
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, file.Name))
	c.Header("Content-Type", "application/gzip")
	if resp.ContentLength >= 0 {
		c.Header("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, resp.Body); err != nil {
		if c.Request.Context().Err() == nil {
			log.Printf("下载 OneDrive 备份失败: %v", err)
		}
		c.Abort()
	}
}

func (a *App) openOneDriveBackupContent(ctx context.Context, id string) (oneDriveFile, *http.Response, error) {
	file, err := a.oneDriveBackupByIDContext(ctx, id)
	if err != nil {
		return oneDriveFile{}, nil, err
	}
	token, err := a.accessTokenOneDriveContext(ctx)
	if err != nil {
		return oneDriveFile{}, nil, err
	}
	endpoint := oneDriveGraphBaseURL + "/me/drive/items/" + url.PathEscape(id) + "/content"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return oneDriveFile{}, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := oneDriveStreamingHTTPClient().Do(req)
	if err != nil {
		return oneDriveFile{}, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		requestErr := readOneDriveError(resp)
		resp.Body.Close()
		return oneDriveFile{}, nil, requestErr
	}
	return file, resp, nil
}

// POST /api/admin/maintenance/onedrive/backups/:id/restore — download the
// archive on the server and pass it directly into the same staged restore
// pipeline as a browser upload.
func (a *App) restoreOneDriveBackup(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "备份 ID 为空"})
		return
	}
	if !a.selfUpdating.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "已有维护任务正在进行"})
		return
	}
	releaseMaintenanceLock := true
	defer func() {
		if releaseMaintenanceLock {
			a.selfUpdating.Unlock()
		}
	}()
	if !a.oneDriveSyncMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "已有 OneDrive 备份任务正在进行"})
		return
	}
	defer a.oneDriveSyncMu.Unlock()

	file, resp, err := a.openOneDriveBackupContent(c.Request.Context(), id)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errOneDriveBackupNotFound) {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	if file.Size < 0 || file.Size > maxBackupUpload || resp.ContentLength > maxBackupUpload {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "OneDrive 备份文件过大"})
		return
	}
	restartScheduled := a.restoreBackupReader(c, io.LimitReader(resp.Body, maxBackupUpload+1))
	releaseMaintenanceLock = !restartScheduled
}

// DELETE /api/admin/maintenance/onedrive/backups/:id
func (a *App) deleteOneDriveBackup(c *gin.Context) {
	unlockMaintenance, ok := a.tryMaintenanceRequest(c)
	if !ok {
		return
	}
	defer unlockMaintenance()

	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "备份 ID 为空"})
		return
	}
	if _, err := a.oneDriveBackupByIDContext(c.Request.Context(), id); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errOneDriveBackupNotFound) {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	token, err := a.accessTokenOneDriveContext(c.Request.Context())
	if err == nil {
		err = oneDriveGraphJSON(c.Request.Context(), http.MethodDelete, oneDriveGraphBaseURL+"/me/drive/items/"+url.PathEscape(id), token, nil, nil)
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (a *App) runOneDriveBackupScheduler(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			a.pruneOneDrivePending(now)
			settings, err := a.loadOneDriveSettings()
			if err != nil || !settings.AutoSync || settings.RefreshToken == "" {
				continue
			}
			if settings.LastSyncAt != nil && time.Since(*settings.LastSyncAt) < oneDriveSyncInterval {
				continue
			}
			if settings.LastAttemptAt != nil && settings.FailedAttempts > 0 {
				elapsed := now.Sub(*settings.LastAttemptAt)
				if elapsed >= 0 && elapsed < oneDriveRetryDelay(settings.FailedAttempts) {
					continue
				}
			}
			if !a.selfUpdating.TryLock() {
				continue
			}
			if !a.oneDriveSyncMu.TryLock() {
				a.selfUpdating.Unlock()
				continue
			}
			_, err = a.syncOneDriveBackupLockedContext(ctx)
			a.oneDriveSyncMu.Unlock()
			a.selfUpdating.Unlock()
			if err != nil {
				log.Printf("OneDrive 自动备份失败: %v", err)
			}
		}
	}
}

func (a *App) pruneOneDrivePending(now time.Time) {
	a.oneDriveMu.Lock()
	defer a.oneDriveMu.Unlock()
	for sessionID, session := range a.oneDrivePending {
		if now.After(session.ExpiresAt) {
			delete(a.oneDrivePending, sessionID)
		}
	}
}
