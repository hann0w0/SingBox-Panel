package panel

import (
	"context"
	"log"
	"net/http"
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
	cfg       config.PanelConfig
	db        *gorm.DB
	hub       *Hub
	auth      *Auth
	orch      *Orchestrator
	host      *hostStats
	engine    *gin.Engine
	startedAt time.Time
	login     *loginGuard
	version   string
	cfgPath   string

	// selfUpdating serializes panel self-update requests so two admins cannot
	// launch competing binary swaps at once.
	selfUpdating sync.Mutex
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
		cfg:       cfg,
		db:        db,
		hub:       hub,
		auth:      NewAuth(cfg.JWTSecret, db),
		orch:      orch,
		host:      &hostStats{},
		startedAt: time.Now(),
		login:     newLoginGuard(),
	}
	a.engine = a.routes()
	return a
}

func (a *App) routes() *gin.Engine {
	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery(), gin.Logger(), corsMiddleware())

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
		admin.GET("/servers/:id/logs", a.serverLogs)
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
		admin.DELETE("/users/:id", a.deleteUser)

		// Panel maintenance: report/update the panel's own version and export
		// a full data backup (SQLite DB + jwt_secret) for migration.
		admin.GET("/maintenance/info", a.maintenanceInfo)
		admin.POST("/maintenance/update", a.selfUpdate)
		admin.GET("/maintenance/backup", a.downloadBackup)
		admin.POST("/maintenance/restore", a.restoreBackup)
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
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		index := filepath.Join(dir, "index.html")
		if fileExists(index) {
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

	srv := &http.Server{Addr: a.cfg.Listen, Handler: a.engine}
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

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
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
