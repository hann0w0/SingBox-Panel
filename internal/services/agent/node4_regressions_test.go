package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func TestAlpineReleaseInstallerFailsClosedWithoutDigestWhenAPIConfigured(t *testing.T) {
	archive := buildSingboxTarGz(t, "sing-box-1.14.4-linux-amd64-musl", "#!/bin/sh\necho 'sing-box version 1.14.4'\n")
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/SagerNet/sing-box/releases/tags/v1.14.4":
			_ = json.NewEncoder(w).Encode(githubReleasePayload{
				TagName: "v1.14.4",
				Assets: []githubReleaseAsset{
					{Name: "sing-box-1.14.4-linux-amd64-musl.tar.gz", BrowserDownloadURL: server.URL + "/assets/archive.tar.gz"},
				},
			})
		case "/assets/archive.tar.gz":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	installer := alpineReleaseInstaller{
		httpClient:      server.Client(),
		apiBaseURL:      server.URL,
		downloadBaseURL: server.URL,
		goarch:          "amd64",
		targetBinary:    filepath.Join(t.TempDir(), "sing-box"),
		configDir:       filepath.Join(t.TempDir(), "etc"),
		stateDir:        filepath.Join(t.TempDir(), "var"),
		run: func(context.Context, string, ...string) (string, error) {
			t.Fatal("must not execute downloaded binary when SHA256 digest is missing")
			return "", nil
		},
	}
	if _, err := installer.install(context.Background(), protocol.ChannelStable, "1.14.4"); err == nil || !strings.Contains(err.Error(), "missing SHA256 digest") {
		t.Fatalf("expected fail-closed missing digest error, got: %v", err)
	}
}

func TestMutatingCommandSurvivesDisconnectWithoutStarvingReadOnlyCommands(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	conns := make(chan *websocket.Conn, 4)
	statusReplies := make(chan protocol.Envelope, 4)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conns <- conn
		for {
			var env protocol.Envelope
			if err := conn.ReadJSON(&env); err != nil {
				return
			}
			if env.Type == protocol.EvtCommandResult {
				statusReplies <- env
			}
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", false)
	client.wsURL = "ws" + strings.TrimPrefix(server.URL, "http")
	client.heartbeatInterval = time.Minute
	client.IsMutatingCommand = commandMutates

	mutStarted := make(chan struct{})
	mutRelease := make(chan struct{})
	var mutCtxErr error
	var mutDone sync.WaitGroup
	mutDone.Add(1)

	client.OnCommand = func(ctx context.Context, env protocol.Envelope) protocol.CommandResultEvt {
		if env.Type == protocol.CmdInstallSingbox {
			defer mutDone.Done()
			close(mutStarted)
			<-mutRelease
			mutCtxErr = ctx.Err()
			return protocol.CommandResultEvt{ID: env.ID, OK: true}
		}
		return protocol.CommandResultEvt{ID: env.ID, OK: true}
	}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()
	go client.Run(rootCtx)

	var conn1 *websocket.Conn
	select {
	case conn1 = <-conns:
	case <-time.After(3 * time.Second):
		t.Fatal("agent client did not connect")
	}

	mutEnv, _ := protocol.NewEnvelope(protocol.CmdInstallSingbox, "mut-1", protocol.InstallSingboxCmd{})
	_ = conn1.WriteJSON(mutEnv)
	<-mutStarted

	// While mutating worker is blocked, read-only command must still execute immediately.
	statusEnv, _ := protocol.NewEnvelope(protocol.CmdGetStatus, "status-1", struct{}{})
	_ = conn1.WriteJSON(statusEnv)
	select {
	case reply := <-statusReplies:
		if reply.ID != "status-1" {
			t.Fatalf("unexpected reply %+v", reply)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read-only command starved behind active mutating command")
	}

	// Simulate transient WebSocket disconnect; mutating context must remain alive until rootCtx cancels.
	_ = conn1.Close()
	select {
	case <-conns:
	case <-time.After(3 * time.Second):
		t.Fatal("agent client did not reconnect")
	}

	close(mutRelease)
	mutDone.Wait()
	if mutCtxErr != nil {
		t.Fatalf("mutating command context was canceled by WS disconnect: %v", mutCtxErr)
	}
}

func TestIPv6OnlyAddressProbeRespectsCooldownAndRunUsesSIGTERM(t *testing.T) {
	a := Agent{}
	now := time.Now()
	v6 := "2001:db8::9"
	a.addressMu.Lock()
	a.addressCached = publicAddresses{IPv6: &v6}
	a.addressProbeAt = now
	a.addressMu.Unlock()

	if got := a.publicAddresses(context.Background(), false); got.IPv4 != nil || got.IPv6 != nil {
		t.Fatalf("expected cooldown to skip duplicate probe, got %+v", got)
	}

	// Verify run cancels with SIGTERM (catching signal inside subprocess before exiting).
	sigFile := filepath.Join(t.TempDir(), "sig.txt")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := run(ctx, "sh", "-c", `trap 'echo TERM > "$1"; exit 0' TERM; while true; do sleep 0.05; done`, "sh", sigFile)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("run cancel timed out")
	}
	raw, err := os.ReadFile(sigFile)
	if err != nil || strings.TrimSpace(string(raw)) != "TERM" {
		t.Fatalf("expected SIGTERM trap output, got %q err=%v", string(raw), err)
	}
}
