// Command panel is the SingPanel control-plane server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"syscall"

	"github.com/singpanel/singpanel/internal/config"
	"github.com/singpanel/singpanel/internal/panel"
)

// version is set via -ldflags "-X main.version=x.y.z".
var version = "dev"

func main() {
	cfgPath := flag.String("config", "", "path to panel config YAML")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVer {
		fmt.Println("singpanel", version)
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("singpanel %s starting", version)
	if err := app.Run(ctx); err != nil {
		log.Fatalf("server: %v", err)
	}
	log.Println("singpanel stopped")
}
