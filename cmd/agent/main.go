// Command agent is the SingBox Panel VPS-side daemon. It reverse-connects to the
// panel and manages the official sing-box on the host.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/hann0w0/singbox-panel/internal/agent"
)

// version is set via -ldflags "-X main.version=x.y.z".
var version = "v1.0.3"

func main() {
	var (
		urlFlag   = flag.String("url", "", "panel base URL, e.g. https://panel.example.com")
		tokenFlag = flag.String("token", "", "agent token")
		confFlag  = flag.String("config", "/etc/singbox-panel-agent/agent.conf", "agent config file (KEY=VALUE)")
		insecure  = flag.Bool("insecure", false, "skip TLS verification (self-signed panel)")
		showVer   = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("singbox-panel-agent", version)
		return
	}

	fileConf := readConfFile(*confFlag)

	panelURL := firstNonEmpty(*urlFlag, os.Getenv("SINGBOX_PANEL_URL"), fileConf["URL"])
	token := firstNonEmpty(*tokenFlag, os.Getenv("SINGBOX_PANEL_TOKEN"), fileConf["TOKEN"])
	insecureVal := *insecure || strings.EqualFold(fileConf["INSECURE"], "true") ||
		strings.EqualFold(os.Getenv("SINGBOX_PANEL_INSECURE"), "true")

	if panelURL == "" || token == "" {
		log.Fatal("agent: missing panel URL or token (set --url/--token, env SINGBOX_PANEL_URL/SINGBOX_PANEL_TOKEN, or the config file)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("singbox-panel-agent %s starting; panel=%s", version, panelURL)
	a := agent.New(agent.Config{
		PanelURL:     panelURL,
		Token:        token,
		Insecure:     insecureVal,
		AgentVersion: version,
	})
	a.Run(ctx)
	log.Println("singbox-panel-agent stopped")
}

func readConfFile(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.ToUpper(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
