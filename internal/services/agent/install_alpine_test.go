package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func buildSingboxTarGz(t *testing.T, dirName, binaryContent string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	data := []byte(binaryContent)
	hdr := &tar.Header{
		Name: dirName + "/sing-box",
		Mode: 0o755,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAlpineReleaseInstallerStableBetaAndVersionPin(t *testing.T) {
	stableArchive := buildSingboxTarGz(t, "sing-box-1.14.4-linux-amd64-musl", "#!/bin/sh\necho 'sing-box version 1.14.4'\n")
	betaArchive := buildSingboxTarGz(t, "sing-box-1.15.0-alpha.13-linux-amd64-musl", "#!/bin/sh\necho 'sing-box version 1.15.0-alpha.13'\n")
	fallbackArchive := buildSingboxTarGz(t, "sing-box-1.13.9-linux-arm64", "#!/bin/sh\necho 'sing-box version 1.13.9'\n")

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/SagerNet/sing-box/releases/latest":
			_ = json.NewEncoder(w).Encode(githubReleasePayload{
				TagName: "v1.14.4",
				Assets: []githubReleaseAsset{
					{Name: "sing-box-1.14.4-linux-amd64-musl.tar.gz", BrowserDownloadURL: server.URL + "/assets/stable-musl.tar.gz"},
				},
			})
		case "/repos/SagerNet/sing-box/releases":
			_ = json.NewEncoder(w).Encode([]githubReleasePayload{
				{
					TagName: "v1.15.0-alpha.13",
					Assets: []githubReleaseAsset{
						{Name: "sing-box-1.15.0-alpha.13-linux-amd64-musl.tar.gz", BrowserDownloadURL: server.URL + "/assets/beta-musl.tar.gz"},
					},
				},
			})
		case "/assets/stable-musl.tar.gz":
			_, _ = w.Write(stableArchive)
		case "/assets/beta-musl.tar.gz":
			_, _ = w.Write(betaArchive)
		case "/SagerNet/sing-box/releases/download/v1.13.9/sing-box-1.13.9-linux-arm64-musl.tar.gz":
			http.NotFound(w, r)
		case "/SagerNet/sing-box/releases/download/v1.13.9/sing-box-1.13.9-linux-arm64.tar.gz":
			_, _ = w.Write(fallbackArchive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tests := []struct {
		name        string
		goarch      string
		channel     string
		version     string
		wantContent string
	}{
		{"stable latest musl", "amd64", protocol.ChannelStable, "", "1.14.4"},
		{"beta latest musl", "amd64", protocol.ChannelBeta, "", "1.15.0-alpha.13"},
		{"pinned version falls back when musl archive is absent", "arm64", protocol.ChannelStable, "1.13.9", "1.13.9"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			targetBin := filepath.Join(root, "usr", "bin", "sing-box")
			configDir := filepath.Join(root, "etc", "sing-box")
			stateDir := filepath.Join(root, "var", "lib", "sing-box")

			verifiedPath := ""
			installer := alpineReleaseInstaller{
				httpClient:      server.Client(),
				apiBaseURL:      server.URL,
				downloadBaseURL: server.URL,
				goarch:          tc.goarch,
				targetBinary:    targetBin,
				configDir:       configDir,
				stateDir:        stateDir,
				run: func(_ context.Context, name string, args ...string) (string, error) {
					if len(args) == 1 && args[0] == "version" {
						verifiedPath = name
						return "sing-box version " + tc.wantContent, nil
					}
					return "", nil
				},
			}
			out, err := installer.install(context.Background(), tc.channel, tc.version)
			if err != nil {
				t.Fatalf("install failed: %v\noutput:\n%s", err, out)
			}
			if verifiedPath == "" {
				t.Fatal("staged binary was not verified with `version` before install")
			}
			raw, err := os.ReadFile(targetBin)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), tc.wantContent) {
				t.Fatalf("installed binary = %q, want %q", string(raw), tc.wantContent)
			}
			for _, dir := range []string{configDir, stateDir} {
				if info, err := os.Stat(dir); err != nil || !info.IsDir() {
					t.Fatalf("expected directory %s to exist: err=%v", dir, err)
				}
			}
		})
	}
}

func TestAlpineReleaseInstallerPreservesConfigWhenRemovingAPKPackages(t *testing.T) {
	archive := buildSingboxTarGz(t, "sing-box-1.14.4-linux-amd64-musl", "binary-1.14.4")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "sing-box-1.14.4-linux-amd64-musl.tar.gz") {
			_, _ = w.Write(archive)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	root := t.TempDir()
	targetBin := filepath.Join(root, "usr", "bin", "sing-box")
	configDir := filepath.Join(root, "etc", "sing-box")
	stateDir := filepath.Join(root, "var", "lib", "sing-box")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"log":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "cache.db"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}

	installedPkgs := map[string]bool{"sing-box": true, "sing-box-openrc": true}
	var calls []string
	installer := alpineReleaseInstaller{
		httpClient:      server.Client(),
		downloadBaseURL: server.URL,
		goarch:          "amd64",
		targetBinary:    targetBin,
		configDir:       configDir,
		stateDir:        stateDir,
		hasAPK:          func() bool { return true },
		hasRCService:    func() bool { return true },
		run: func(_ context.Context, name string, args ...string) (string, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			if name == "apk" && len(args) == 3 && args[0] == "info" && args[1] == "-e" {
				if installedPkgs[args[2]] {
					return args[2], nil
				}
				return "", os.ErrNotExist
			}
			if name == "apk" && len(args) == 2 && args[0] == "del" {
				delete(installedPkgs, args[1])
				// Simulate apk del wiping /etc/sing-box and /var/lib/sing-box.
				_ = os.RemoveAll(configDir)
				_ = os.RemoveAll(stateDir)
				return "removed " + args[1], nil
			}
			return "sing-box version 1.14.4", nil
		},
	}

	if _, err := installer.install(context.Background(), protocol.ChannelStable, "1.14.4"); err != nil {
		t.Fatal(err)
	}
	if len(installedPkgs) != 0 {
		t.Fatalf("expected all apk sing-box packages removed, remaining: %v", installedPkgs)
	}
	cfg, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil || string(cfg) != `{"log":{}}` {
		t.Fatalf("config.json was not restored: %q err=%v", string(cfg), err)
	}
	state, err := os.ReadFile(filepath.Join(stateDir, "cache.db"))
	if err != nil || string(state) != "state" {
		t.Fatalf("state cache.db was not restored: %q err=%v", string(state), err)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "rc-service sing-box stop") {
		t.Fatalf("expected rc-service stop before apk del, calls:\n%s", joined)
	}
}

func TestSingboxInstallerDispatchesAlpineAndRestartsActiveService(t *testing.T) {
	alpineCalled := false
	prepared := false
	restarted := false

	installer := singboxInstaller{
		isAlpine: func() bool { return true },
		installAlpine: func(_ context.Context, channel, version string) (string, error) {
			alpineCalled = true
			if channel != protocol.ChannelBeta || version != "1.15.0-alpha.13" {
				t.Fatalf("unexpected alpine args: channel=%q version=%q", channel, version)
			}
			return "installed musl release", nil
		},
		detectVersion: func(context.Context) (bool, string) { return true, "1.15.0-alpha.13" },
		prepareService: func(context.Context) error {
			prepared = true
			return nil
		},
		serviceActive: func(context.Context) bool { return true },
		restartService: func(context.Context) error {
			restarted = true
			return nil
		},
	}

	out, err := installer.install(context.Background(), protocol.ChannelBeta, "1.15.0-alpha.13", protocol.MethodScript)
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}
	if !alpineCalled || !prepared || !restarted || !strings.Contains(out, "installed musl release") {
		t.Fatalf("alpine=%v prepared=%v restarted=%v out=%q", alpineCalled, prepared, restarted, out)
	}
}

func TestAlpineArchiveVerifiesDigestAndRejectsEscapingSymlink(t *testing.T) {
	if got := parseSHA256Digest("sha256:" + strings.Repeat("a", 64)); got != strings.Repeat("a", 64) {
		t.Fatalf("parseSHA256Digest = %q", got)
	}
	if got := parseSHA256Digest("sha256:not-hex"); got != "" {
		t.Fatalf("invalid digest accepted: %q", got)
	}

	src := t.TempDir()
	dst := t.TempDir()
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := copyDirTree(src, dst, false); err == nil || !strings.Contains(err.Error(), "pointing outside") {
		t.Fatalf("escaping symlink should be rejected, got %v", err)
	}
}
