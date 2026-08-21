// Command panel is the SingBox Panel control-plane server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"syscall"

	"github.com/hann0w0/singbox-panel/internal/config"
	"github.com/hann0w0/singbox-panel/internal/panel"
)

// version is set via -ldflags "-X main.version=x.y.z".
var version = "v1.0.8"

func main() {
	cfgPath := flag.String("config", "", "path to panel config YAML")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVer {
		fmt.Println("singbox-panel", version)
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config security validation: %v", err)
	}

	secret, err := panel.ResolveJWTSecret(cfg)
	if err != nil {
		log.Fatalf("jwt secret: %v", err)
	}
	cfg.JWTSecret = secret

	db, err := panel.InitDB(cfg)
	if err != nil {
		log.Fatalf("database: %v", err)
	}

	app := panel.NewApp(cfg, db)
	app.SetVersion(version)
	app.SetConfigPath(*cfgPath)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("singbox-panel %s starting", version)
	if err := app.Run(ctx); err != nil {
		log.Fatalf("server: %v", err)
	}
	log.Println("singbox-panel stopped")
}
