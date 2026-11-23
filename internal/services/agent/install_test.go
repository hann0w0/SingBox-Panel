package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func TestInstallSingboxPropagatesDownloadFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte("#!/bin/sh\necho mock-download-failed >&2\nexit 22\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMPDIR", dir)
	output, err := InstallSingbox(context.Background(), protocol.ChannelStable, "", "script")
	if err == nil || !strings.Contains(err.Error(), "download sing-box installer") || !strings.Contains(output, "mock-download-failed") {
		t.Fatalf("download failure was swallowed: output=%q error=%v", output, err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "singbox-install-*.sh"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary installer files remain: %v, %v", files, err)
	}
}

func TestInstallSingboxPropagatesInstallerFailure(t *testing.T) {
	dir := t.TempDir()
	// This curl fixture writes only a harmless local script; no remote installer
	// or package manager is invoked by this test.
	fixture := "#!/bin/sh\nprintf '#!/bin/sh\\necho mock-install-failed >&2\\nexit 9\\n' > \"$3\"\n"
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := InstallSingbox(context.Background(), protocol.ChannelBeta, "1.14.0-alpha.1", "script")
	if err == nil || !strings.Contains(err.Error(), "run sing-box installer") || !strings.Contains(output, "mock-install-failed") {
		t.Fatalf("installer failure was swallowed: output=%q error=%v", output, err)
	}
}

func TestInstallSingboxRejectsEmptyDownload(t *testing.T) {
	executed := false
	installer := singboxInstaller{
		run: func(_ context.Context, command string, _ ...string) (string, error) {
			if command != "curl" {
				executed = true
			}
			return "", nil
		},
		detectVersion: func(context.Context) (bool, string) {
			t.Fatal("empty installer must not reach version verification")
			return true, "1.14.0"
		},
	}
	_, err := installer.install(context.Background(), protocol.ChannelStable, "", "script")
	if err == nil || !strings.Contains(err.Error(), "empty") || executed {
		t.Fatalf("empty download was executed: %v", err)
	}
}

func TestAllSingboxInstallMethodsVerifyBinaryAndRequestedVersion(t *testing.T) {
	for _, method := range []string{"script", "apt", "dnf"} {
		for _, tc := range []struct {
			name      string
			installed bool
			actual    string
			requested string
			wantError bool
		}{
			{"missing", false, "", "", true},
			{"broken", true, "", "", true},
			{"invalid version", true, "not a version", "", true},
			{"wrong version", true, "1.13.0", "1.14.0", true},
			{"expected version", true, "1.14.0", "1.14.0", false},
			{"latest beta", true, "1.14.0-alpha.1", "", false},
		} {
			if method != "script" && tc.requested != "" {
				continue // Package methods reject version pins before installation.
			}
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				installer := singboxInstaller{
					run: func(_ context.Context, command string, args ...string) (string, error) {
						if command == "curl" {
							return "", os.WriteFile(args[2], []byte("#!/bin/sh\nexit 0\n"), 0o600)
						}
						return "mock installation complete", nil
					},
					detectVersion: func(context.Context) (bool, string) { return tc.installed, tc.actual },
				}
				_, err := installer.install(context.Background(), protocol.ChannelBeta, tc.requested, method)
				if (err != nil) != tc.wantError {
					t.Fatalf("verification error = %v, want error = %t", err, tc.wantError)
				}
			})
		}
	}
}

func TestPackageInstallRejectsVersionPinBeforeCommands(t *testing.T) {
	for _, method := range []string{protocol.MethodAPT, protocol.MethodDNF} {
		t.Run(method, func(t *testing.T) {
			installer := singboxInstaller{
				run: func(context.Context, string, ...string) (string, error) {
					t.Fatal("a rejected version pin must not execute any system command")
					return "", nil
				},
				detectVersion: func(context.Context) (bool, string) {
					t.Fatal("a rejected version pin must not reach post-install verification")
					return true, "1.14.0"
				},
			}
			_, err := installer.install(context.Background(), protocol.ChannelStable, "1.14.0", method)
			if err == nil || !strings.Contains(err.Error(), "script") {
				t.Fatalf("version pin must be rejected with script-method guidance: %v", err)
			}
		})
	}
}

func TestPackageInstallFailureSkipsVersionCheck(t *testing.T) {
	for _, method := range []string{"apt", "dnf"} {
		t.Run(method, func(t *testing.T) {
			failure := errors.New("mock package manager failure")
			installer := singboxInstaller{
				run: func(context.Context, string, ...string) (string, error) { return "failed", failure },
				detectVersion: func(context.Context) (bool, string) {
					t.Fatal("failed package manager must not be rescued by an existing binary")
					return true, "1.14.0"
				},
			}
			_, err := installer.install(context.Background(), protocol.ChannelStable, "", method)
			if !errors.Is(err, failure) {
				t.Fatalf("failure = %v", err)
			}
		})
	}
}

func TestSingboxInstallerUsesCommandDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	steps := 0
	installer := singboxInstaller{
		run: func(commandCtx context.Context, command string, args ...string) (string, error) {
			if commandCtx != ctx {
				t.Fatal("installation command lost its timeout context")
			}
			steps++
			if command == "curl" {
				return "", os.WriteFile(args[2], []byte("#!/bin/sh\nexit 0\n"), 0o600)
			}
			cancel()
			return "", nil
		},
		detectVersion: func(context.Context) (bool, string) {
			t.Fatal("canceled installer must not report success")
			return true, "1.14.0"
		},
	}
	_, err := installer.install(ctx, protocol.ChannelStable, "", "script")
	if !errors.Is(err, context.Canceled) || steps != 2 {
		t.Fatalf("cancellation not propagated: steps=%d error=%v", steps, err)
	}
}

func TestSingboxInstallerPreparesHostServiceAfterVerification(t *testing.T) {
	prepared := 0
	installer := singboxInstaller{
		run: func(_ context.Context, command string, args ...string) (string, error) {
			if command == "curl" {
				return "", os.WriteFile(args[2], []byte("#!/bin/sh\nexit 0\n"), 0o600)
			}
			return "", nil
		},
		detectVersion: func(context.Context) (bool, string) { return true, "1.14.0" },
		prepareService: func(context.Context) error {
			prepared++
			return nil
		},
	}
	if _, err := installer.install(context.Background(), protocol.ChannelStable, "", "script"); err != nil {
		t.Fatal(err)
	}
	if prepared != 1 {
		t.Fatalf("service preparation count = %d, want 1", prepared)
	}
}

func TestSingboxInstallerSurfacesServicePreparationFailure(t *testing.T) {
	failure := errors.New("openrc unavailable")
	installer := singboxInstaller{
		run: func(_ context.Context, command string, args ...string) (string, error) {
			if command == "curl" {
				return "", os.WriteFile(args[2], []byte("#!/bin/sh\nexit 0\n"), 0o600)
			}
			return "", nil
		},
		detectVersion: func(context.Context) (bool, string) { return true, "1.14.0" },
		prepareService: func(context.Context) error {
			return failure
		},
	}
	_, err := installer.install(context.Background(), protocol.ChannelStable, "", "script")
	if !errors.Is(err, failure) {
		t.Fatalf("service preparation failure = %v", err)
	}
}

func TestPrepareSingboxServiceEnablesBootStart(t *testing.T) {
	enabled := false
	manager := systemdServiceManager{run: func(_ context.Context, command string, args ...string) (string, error) {
		if command == "systemctl" && len(args) > 0 {
			switch args[0] {
			case "daemon-reload":
				return "", nil
			case "is-enabled":
				if enabled {
					return "enabled", nil
				}
				return "disabled", errors.New("disabled")
			case "enable":
				enabled = true
				return "", nil
			}
		}
		return "", nil
	}}
	if err := prepareSingboxServiceWithManager(context.Background(), manager); err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("sing-box service was not enabled at boot")
	}
}

func TestInstallScriptHardensCurlFlagsAndValidatesPayload(t *testing.T) {
	var curlArgs []string
	installer := singboxInstaller{
		run: func(_ context.Context, command string, args ...string) (string, error) {
			if command == "curl" {
				curlArgs = append([]string(nil), args...)
				return "", os.WriteFile(args[2], []byte("#!/bin/sh\x00binary"), 0o600)
			}
			t.Fatal("binary payload must not be executed")
			return "", nil
		},
		detectVersion: func(context.Context) (bool, string) { return true, "1.14.0" },
	}
	_, err := installer.install(context.Background(), protocol.ChannelStable, "", protocol.MethodScript)
	if err == nil || !strings.Contains(err.Error(), "valid shell script") {
		t.Fatalf("expected invalid script error, got %v", err)
	}
	joined := strings.Join(curlArgs, " ")
	for _, want := range []string{"--proto =https", "--proto-redir =https", "--max-redirs 5", "--max-filesize"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("curl args missing %q: %s", want, joined)
		}
	}

	t.Setenv("SINGBOX_INSTALL_SCRIPT_SHA256", strings.Repeat("0", 64))
	installer2 := singboxInstaller{
		run: func(_ context.Context, command string, args ...string) (string, error) {
			if command == "curl" {
				return "", os.WriteFile(args[2], []byte("#!/bin/sh\nexit 0\n"), 0o600)
			}
			t.Fatal("mismatched pinned SHA256 must not execute script")
			return "", nil
		},
		detectVersion: func(context.Context) (bool, string) { return true, "1.14.0" },
	}
	_, err = installer2.install(context.Background(), protocol.ChannelStable, "", protocol.MethodScript)
	if err == nil || !strings.Contains(err.Error(), "SHA256 mismatch") {
		t.Fatalf("expected SHA256 mismatch error, got %v", err)
	}
}
