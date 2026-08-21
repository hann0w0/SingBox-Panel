package panel

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/config"
)

// App is the panel HTTP server and its dependencies.
type App struct {
	cfg                 config.PanelConfig
	db                  *gorm.DB
	hub                 *Hub
	auth                *Auth
	orch                *Orchestrator
	host                *hostStats
	engine              *gin.Engine
	startedAt           time.Time
	login               *loginGuard
	customSubscriptions *customNodeSubscriptionManager
	wsLimiter           *wsHandshakeLimiter
	wsLimiterMu         sync.Mutex
	version             string
	cfgPath             string

	// selfUpdating serializes panel self-update requests so two admins cannot
	// launch competing binary swaps at once.
	selfUpdating sync.Mutex

	// OneDrive device-code sessions are short-lived and never persisted. The
	// refresh token is stored encrypted in the existing settings table.
	oneDriveMu      sync.Mutex
	oneDrivePending map[string]oneDriveDeviceSession
	oneDriveSyncMu  sync.Mutex
	// Settings updates are stored as one JSON value. Serialize read-modify-write
	// operations so token rotation cannot overwrite sync metadata (or vice versa).
	oneDriveSettingsMu sync.Mutex
	oneDriveTokenMu    sync.Mutex
}

// SetVersion records the running panel version (set from main via ldflags) so
// the maintenance API can report it and compare against the latest release.
func (a *App) SetVersion(v string) { a.version = v }

// SetConfigPath records the panel.yaml path (from the --config flag) so the
// restore flow can rewrite jwt_secret when importing a backup from another host.
func (a *App) SetConfigPath(p string) { a.cfgPath = p }

// NewApp wires the panel components and builds the router.
func NewApp(cfg config.PanelConfig, db *gorm.DB) *App {
	hub := NewHub(db)
	orch := NewOrchestrator(db, hub)
	hub.AfterRegister = func(serverID uint) { orch.PushConfigIfManaged(serverID) }

	a := &App{
		cfg:                 cfg,
		db:                  db,
		hub:                 hub,
		auth:                NewAuth(cfg.JWTSecret, db),
		orch:                orch,
		host:                &hostStats{},
		startedAt:           time.Now(),
		login:               newLoginGuard(),
		customSubscriptions: newCustomNodeSubscriptionManager(db),
		wsLimiter:           newWSHandshakeLimiter(),
		oneDrivePending:     make(map[string]oneDriveDeviceSession),
	}
	a.engine = a.routes()
	return a
}

func (a *App) routes() *gin.Engine {
	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery(), panelRequestLogger(a.cfg.Subscription.PathPrefix), securityHeadersMiddleware(a.cfg.BaseURL), corsMiddleware(a.cfg.BaseURL, a.cfg.Environment))

	// Public endpoints.
	r.GET("/api/agent/install.sh", a.handleAgentInstallScript)
	r.GET("/api/agent/download", a.handleAgentDownload)
	r.GET("/api/agent/checksum", a.handleAgentChecksum)
	r.GET("/api/agent/ws", a.handleAgentWS)
	r.POST("/api/auth/login", a.handleLogin)
	r.GET(a.cfg.Subscription.PathPrefix+"/:token", a.handleSubscription)
	r.GET("/api/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	// Authenticated user endpoints.
	user := r.Group("/api")
	user.Use(a.auth.Middleware())
	{
		user.GET("/user/me", a.handleMe)
		user.GET("/user/nodes", a.handleUserNodes)
		user.POST("/user/reset-sub", a.handleResetSub)
		user.POST("/user/change-password", a.handleChangePassword)
	}

	// Admin endpoints.
	admin := r.Group("/api/admin")
	admin.Use(a.auth.Middleware(), AdminOnly())
	{
		admin.GET("/overview", a.adminOverview)

		admin.GET("/servers", a.listServers)
		admin.POST("/servers", a.createServer)
		admin.PUT("/servers/order", a.updateServerOrder)
		admin.GET("/servers/:id", a.getServer)
		admin.PUT("/servers/:id", a.updateServer)
		admin.DELETE("/servers/:id", a.deleteServer)
		admin.POST("/servers/:id/install-singbox", a.installSingbox)
		admin.POST("/servers/:id/uninstall-agent", a.uninstallAgent)
		admin.POST("/servers/:id/service", a.serviceAction)
		admin.POST("/servers/:id/update-agent", a.updateAgent)
		admin.POST("/servers/update-all-agents", a.updateAllAgents)
		admin.GET("/servers/:id/status", a.serverStatus)
		admin.GET("/servers/:id/traffic", a.serverTraffic)
		admin.GET("/servers/:id/traffic/live", a.handleTrafficSSE)
		admin.GET("/servers/:id/logs", a.serverLogs)
		admin.POST("/servers/:id/stream-logs", a.streamLogs)
		admin.GET("/servers/:id/remote-config", a.remoteConfig)
		admin.POST("/servers/:id/apply-raw", a.applyRawConfig)
		admin.POST("/servers/:id/import-config", a.importRemoteConfig)
		admin.POST("/servers/:id/config-mode", a.setConfigMode)
		admin.GET("/servers/:id/node-formats", a.serverNodeFormats)

		admin.GET("/servers/:id/inbounds", a.listInbounds)
		admin.POST("/servers/:id/inbounds", a.createInbound)
		admin.PUT("/servers/:id/inbounds/:inboundID", a.updateInbound)
		admin.DELETE("/servers/:id/inbounds/:inboundID", a.deleteInbound)

		admin.GET("/servers/:id/outbounds", a.listOutbounds)
		admin.POST("/servers/:id/outbounds", a.createOutbound)
		admin.PUT("/servers/:id/outbounds/:outboundID", a.updateOutbound)
		admin.DELETE("/servers/:id/outbounds/:outboundID", a.deleteOutbound)
		admin.POST("/servers/:id/outbounds/:outboundID/test", a.testOutbound)
		admin.POST("/servers/:id/test-egress", a.testEgress)

		admin.GET("/servers/:id/rules", a.listRules)
		admin.PUT("/servers/:id/rules/reorder", a.reorderRules)
		admin.POST("/servers/:id/rules", a.createRule)
		admin.PUT("/servers/:id/rules/:ruleID", a.updateRule)
		admin.DELETE("/servers/:id/rules/:ruleID", a.deleteRule)

		admin.GET("/servers/:id/rulesets", a.listRuleSets)
		admin.POST("/servers/:id/rulesets", a.createRuleSet)
		admin.PUT("/servers/:id/rulesets/:rulesetID", a.updateRuleSet)
		admin.DELETE("/servers/:id/rulesets/:rulesetID", a.deleteRuleSet)

		admin.PUT("/servers/:id/final-outbound", a.setFinalOutbound)

		admin.GET("/users", a.listUsers)
		admin.POST("/users", a.createUser)
		admin.PUT("/users/:id", a.updateUser)
		admin.GET("/users/:id/access", a.getUserAccess)
		admin.PUT("/users/:id/access", a.updateUserAccess)
		admin.DELETE("/users/:id", a.deleteUser)

		// Hand-added external nodes merged into subscriptions (not managed here).
		admin.GET("/custom-nodes", a.listCustomNodes)
		admin.POST("/custom-nodes", a.createCustomNode)
		admin.POST("/custom-nodes/import/preview", a.previewCustomNodeImport)
		admin.POST("/custom-nodes/import", a.importCustomNodes)
		admin.PUT("/custom-nodes/:id", a.updateCustomNode)
		admin.DELETE("/custom-nodes/:id", a.deleteCustomNode)
		admin.POST("/custom-nodes/batch-delete", a.batchDeleteCustomNodes)
		admin.POST("/custom-nodes/batch-group", a.batchSetCustomNodeGroup)
		admin.GET("/custom-node-subscriptions", a.listCustomNodeSubscriptions)
		admin.POST("/custom-node-subscriptions", a.createCustomNodeSubscription)
		admin.PUT("/custom-node-subscriptions/:id", a.updateCustomNodeSubscription)
		admin.DELETE("/custom-node-subscriptions/:id", a.deleteCustomNodeSubscription)
		admin.POST("/custom-node-subscriptions/:id/sync", a.syncCustomNodeSubscription)

		// Panel maintenance: report/update the panel's own version and export
		// a full data backup (SQLite DB + jwt_secret) for migration.
		admin.GET("/maintenance/info", a.maintenanceInfo)
		admin.POST("/maintenance/update", a.selfUpdate)
		admin.GET("/maintenance/backup", a.downloadBackup)
		admin.POST("/maintenance/restore", a.restoreBackup)
		admin.GET("/maintenance/onedrive", a.oneDriveStatus)
		admin.PUT("/maintenance/onedrive", a.updateOneDriveSettings)
		admin.POST("/maintenance/onedrive/auth/start", a.startOneDriveAuth)
		admin.POST("/maintenance/onedrive/auth/:sessionID/poll", a.pollOneDriveAuth)
		admin.POST("/maintenance/onedrive/sync", a.syncOneDriveBackup)
		admin.GET("/maintenance/onedrive/backups/:id/download", a.downloadOneDriveBackup)
		admin.POST("/maintenance/onedrive/backups/:id/restore", a.restoreOneDriveBackup)
		admin.DELETE("/maintenance/onedrive/backups/:id", a.deleteOneDriveBackup)
	}

	a.mountFrontend(r)
	return r
}

// webDir resolves the built frontend directory from config (or env override
// applied at config-load time).
func (a *App) webDir() string {
	return a.cfg.WebDir
}

func (a *App) mountFrontend(r *gin.Engine) {
	dir := a.webDir()
	for _, staticDir := range []string{"assets", "logos"} {
		path := filepath.Join(dir, staticDir)
		if dirExists(path) {
			r.Static("/"+staticDir, path)
		}
	}
	// Root-level favicon files live next to index.html in the built dist and
	// must be served verbatim — the NoRoute fallback would otherwise answer
	// with index.html (SPA catch-all) and browsers would never see the icon.
	for _, fav := range []string{"favicon.svg", "favicon-32x32.png", "apple-touch-icon.png"} {
		path := filepath.Join(dir, fav)
		if fileExists(path) {
			r.StaticFile("/"+fav, path)
		}
	}
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		index := filepath.Join(dir, "index.html")
		if fileExists(index) {
			// The SPA entry selects content-hashed JavaScript bundles. Never cache
			// index.html itself, otherwise a long-lived admin tab can keep loading an
			// obsolete Users chunk after an in-place deployment.
			c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
			c.Header("Pragma", "no-cache")
			c.Header("Expires", "0")
			c.File(index)
			return
		}
		c.String(http.StatusOK,
			"SingBox Panel backend is running.\nBuild the frontend: cd web && npm install && npm run build")
	})
}

// Run starts the reconciler and HTTP server, shutting down when ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	ResetServersOffline(a.db)

	go a.host.run(ctx)

	rec := NewReconciler(a.db, func(serverIDs []uint) { a.refreshUserProxyAccess(serverIDs) })
	go rec.Run(ctx)
	go a.customSubscriptions.run(ctx)
	go a.runOneDriveBackupScheduler(ctx)

	srv := newPanelHTTPServer(a.cfg.Listen, a.engine)
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("panel listening on %s (base URL %s)", a.cfg.Listen, a.cfg.BaseURL)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

const (
	panelReadHeaderTimeout = 10 * time.Second
	panelIdleTimeout       = 60 * time.Second
)

func newPanelHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: panelReadHeaderTimeout,
		IdleTimeout:       panelIdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
}

func (a *App) allowWSHandshake(key string, now time.Time) time.Duration {
	a.wsLimiterMu.Lock()
	if a.wsLimiter == nil {
		a.wsLimiter = newWSHandshakeLimiter()
	}
	limiter := a.wsLimiter
	a.wsLimiterMu.Unlock()
	return limiter.allow(key, now)
}

func panelRequestLogger(subscriptionPrefix string) gin.HandlerFunc {
	prefix := strings.TrimRight(subscriptionPrefix, "/") + "/"
	return gin.LoggerWithConfig(gin.LoggerConfig{
		SkipQueryString: true,
		Skip: func(c *gin.Context) bool {
			return subscriptionRequestPath(c.Request.URL.Path, prefix)
		},
	})
}

func subscriptionRequestPath(path, prefix string) bool {
	return strings.HasPrefix(path, prefix)
}

func corsMiddleware(baseURL, environment string) gin.HandlerFunc {
	allowedOrigin := originFromBaseURL(baseURL)
	return func(c *gin.Context) {
		requestOrigin := strings.TrimSpace(c.GetHeader("Origin"))
		if requestOrigin != "" {
			c.Header("Vary", "Origin")
			if requestOrigin == allowedOrigin || (strings.EqualFold(environment, "development") && isLoopbackOrigin(requestOrigin)) {
				c.Header("Access-Control-Allow-Origin", allowedOrigin)
				if requestOrigin != allowedOrigin {
					c.Header("Access-Control-Allow-Origin", requestOrigin)
				}
				c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type")
			} else {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func isLoopbackOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.Trim(u.Hostname(), "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func originFromBaseURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func securityHeadersMiddleware(baseURL string) gin.HandlerFunc {
	secureOrigin := strings.HasPrefix(originFromBaseURL(baseURL), "https://")
	return func(c *gin.Context) {
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self' data:; object-src 'none'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if secureOrigin {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Next()
	}
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
