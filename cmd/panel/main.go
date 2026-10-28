// Command panel is the SingBox Panel control-plane server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	_ "time/tzdata" // traffic day boundaries must not depend on the host zoneinfo

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/services/panel"
)

// version is set via -ldflags "-X main.version=x.y.z".
var version = "v1.0.9"

// commit is set via -ldflags "-X main.commit=<40-character SHA>".
var commit string

func main() {
	cfgPath := flag.String("config", "", "path to panel config YAML")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVer {
		if commit != "" {
			fmt.Println("singbox-panel", version, commit)
		} else {
			fmt.Println("singbox-panel", version)
		}
		return
	}

	if commit == "" {
		commit = buildCommit()
	}

	cfg, err := loadRuntimeConfig(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	db, err := panel.InitDB(cfg)
	if err != nil {
		log.Fatalf("database: %v", err)
	}

	app := panel.NewApp(cfg, db)
	app.SetVersion(version)
	app.SetCommit(commit)
	app.SetConfigPath(*cfgPath)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("singbox-panel %s starting", version)
	if err := app.Run(ctx); err != nil {
		log.Fatalf("server: %v", err)
	}
	log.Println("singbox-panel stopped")
}

// buildCommit falls back to Go's VCS stamping for local builds. Release and
// container builds inject it explicitly because they disable VCS stamping.
func buildCommit() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return strings.ToLower(strings.TrimSpace(setting.Value))
		}
	}
	return ""
}

func loadRuntimeConfig(path string) (config.PanelConfig, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("security validation: %w", err)
	}
	if err := panel.RecoverPendingRestore(cfg, path); err != nil {
		return cfg, fmt.Errorf("pending restore recovery: %w", err)
	}
	// Recovery can rewrite the YAML signing key in either the finish or rollback
	// path. Resolve secrets from the recovered configuration, not the old copy.
	cfg, err = config.Load(path)
	if err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("recovered config security validation: %w", err)
	}
	secret, err := panel.ResolveJWTSecret(cfg)
	if err != nil {
		return cfg, fmt.Errorf("jwt secret: %w", err)
	}
	cfg.JWTSecret = secret
	return cfg, nil
}
