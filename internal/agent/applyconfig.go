package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	tmpConfigFile  = ConfigDir + "/.config.json.tmp"
	bakConfigFile  = ConfigDir + "/config.json.bak"
	origConfigFile = ConfigDir + "/config.json.orig"
	maxConfigSize  = 16 << 20
)

// preserveOriginal copies the very first (pre-panel) config to config.json.orig
// exactly once and never overwrites it, so an existing hand-made config is
// always recoverable even after multiple panel applies.
func preserveOriginal() error {
	if _, err := os.Stat(origConfigFile); err == nil {
		return nil // already preserved
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(ConfigFile); err == nil {
		return copyFileOnce(ConfigFile, origConfigFile)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ApplyConfig validates panel-generated config with the official binary, then
// atomically installs it and restarts the service. On any failure it rolls back
// both config.json and every other JSON file moved out of the active directory.
// The command dispatcher serialises mutating operations before calling here.
//
// reload is accepted for wire compatibility but ignored: applying always
// restarts, because SIGHUP reload does not reliably re-read the config.
func ApplyConfig(ctx context.Context, configBytes []byte, reload bool) (string, error) {
	_ = reload
	if len(configBytes) == 0 {
		return "", errors.New("config is empty")
	}
	if len(configBytes) > maxConfigSize {
		return "", fmt.Errorf("config exceeds %d-byte limit", maxConfigSize)
	}
	if _, err := DetectVersionInstalled(); err != nil {
		return "", fmt.Errorf("sing-box not installed at %s", SingboxBinary)
	}
	if err := os.MkdirAll(ConfigDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", ConfigDir, err)
	}
	// Preserve the pre-panel config once (never overwritten) for recovery.
	if err := preserveOriginal(); err != nil {
		return "", fmt.Errorf("preserve original config: %w", err)
	}

	// Nothing to do when the node already runs exactly this config: the panel
	// re-pushes on every agent reconnect (i.e. after every panel restart), and
	// applying would restart sing-box — dropping every live connection — for a
	// byte-identical file.
	if existing, err := os.ReadFile(ConfigFile); err == nil &&
		bytes.Equal(existing, configBytes) && ServiceActive(ctx) {
		return "config unchanged; sing-box left running", nil
	}

	// 1) write temp (extension is .tmp so the -C directory loader ignores it).
	if err := os.WriteFile(tmpConfigFile, configBytes, 0o644); err != nil {
		return "", fmt.Errorf("write temp config: %w", err)
	}

	// 2) validate with the official binary.
	if out, err := run(ctx, SingboxBinary, "check", "-c", tmpConfigFile); err != nil {
		_ = os.Remove(tmpConfigFile)
		return out, fmt.Errorf("sing-box check failed: %w", err)
	}
	previousState := serviceState{active: ServiceActive(ctx), enabled: ServiceEnabled(ctx)}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(tmpConfigFile)
		return "", fmt.Errorf("inspect service state: %w", err)
	}

	// 3) Only after the new config has passed validation, move other active JSON
	// files aside. A failed validation must leave the original directory intact.
	moved, err := moveStrayConfigsAt(ConfigDir)
	if err != nil {
		_ = os.Remove(tmpConfigFile)
		return "", fmt.Errorf("clean config dir: %w", err)
	}

	// 4) backup current config, then atomically replace.
	hadOld := false
	if _, err := os.Stat(ConfigFile); err == nil {
		if err := copyFile(ConfigFile, bakConfigFile); err != nil {
			_ = os.Remove(tmpConfigFile)
			if restoreErr := moved.restore(); restoreErr != nil {
				return "", fmt.Errorf("backup config: %w; restore moved configs: %v", err, restoreErr)
			}
			return "", fmt.Errorf("backup config: %w", err)
		}
		hadOld = true
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(tmpConfigFile)
		if restoreErr := moved.restore(); restoreErr != nil {
			return "", fmt.Errorf("inspect current config: %w; restore moved configs: %v", err, restoreErr)
		}
		return "", fmt.Errorf("inspect current config: %w", err)
	}
	if err := os.Rename(tmpConfigFile, ConfigFile); err != nil {
		_ = os.Remove(tmpConfigFile)
		if restoreErr := moved.restore(); restoreErr != nil {
			return "", fmt.Errorf("install config: %w; restore moved configs: %v", err, restoreErr)
		}
		return "", fmt.Errorf("install config: %w", err)
	}
	if err := os.Chmod(ConfigFile, 0o644); err != nil {
		if rollbackErr := rollbackApply(hadOld, moved, previousState); rollbackErr != nil {
			return "", fmt.Errorf("set config permissions: %w; rollback failed: %v", err, rollbackErr)
		}
		return "", fmt.Errorf("set config permissions: %w", err)
	}

	// 5) apply: restart if running, else enable+start.
	//
	// Always restart, never `systemctl reload`: sing-box's SIGHUP handling does
	// not reliably re-read the config — the command exits 0 while the process
	// keeps serving the OLD config, so a push would silently not take effect.
	// A restart costs a brief connection drop but is guaranteed to apply.
	var applyErr error
	if out, err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		applyErr = fmt.Errorf("systemctl daemon-reload: %w: %s", err, out)
	}
	if applyErr == nil && previousState.active {
		if out, err := run(ctx, "systemctl", "restart", ServiceName); err != nil {
			applyErr = fmt.Errorf("systemctl restart: %w: %s", err, out)
		}
	} else if applyErr == nil {
		if out, err := run(ctx, "systemctl", "enable", ServiceName); err != nil {
			applyErr = fmt.Errorf("systemctl enable: %w: %s", err, out)
		}
	}
	if applyErr == nil && !previousState.active {
		if out, err := run(ctx, "systemctl", "start", ServiceName); err != nil {
			applyErr = fmt.Errorf("systemctl start: %w: %s", err, out)
		}
	}

	// 6) verify the service is active; roll back otherwise.
	if applyErr == nil {
		applyErr = waitServiceStable(ctx, 3*time.Second)
	}
	if applyErr != nil {
		if rollbackErr := rollbackApply(hadOld, moved, previousState); rollbackErr != nil {
			return "", fmt.Errorf("%w; rollback failed: %v", applyErr, rollbackErr)
		}
		return "", applyErr
	}
	return "config applied and service active", nil
}

// rollbackApply restores the previous config and every JSON file moved by this
// apply. It deliberately uses an independent bounded context: the command
// context is commonly cancelled precisely when rollback becomes necessary.
type serviceState struct {
	active  bool
	enabled bool
}

func waitServiceStable(ctx context.Context, stableFor time.Duration) error {
	deadline := time.NewTimer(stableFor)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !ServiceActive(ctx) {
			return errors.New("service did not remain active after apply")
		}
		select {
		case <-deadline.C:
			return nil
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("verify service after apply: %w", ctx.Err())
		}
	}
}

func rollbackApply(hadOld bool, moved *strayMove, previous serviceState) error {
	var errs []error
	if hadOld {
		if err := copyFile(bakConfigFile, ConfigFile); err != nil {
			errs = append(errs, fmt.Errorf("restore config.json: %w", err))
		}
	} else {
		if err := os.Remove(ConfigFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove new config.json: %w", err))
		}
	}
	if err := moved.restore(); err != nil {
		errs = append(errs, err)
	}
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	enableAction := "disable"
	if previous.enabled {
		enableAction = "enable"
	}
	if out, err := run(rollbackCtx, "systemctl", enableAction, ServiceName); err != nil {
		errs = append(errs, fmt.Errorf("restore service enablement with %s: %w: %s", enableAction, err, out))
	}
	action := "stop"
	if previous.active {
		action = "restart"
	}
	if out, err := run(rollbackCtx, "systemctl", action, ServiceName); err != nil {
		errs = append(errs, fmt.Errorf("restore service state with %s: %w: %s", action, err, out))
	}
	return errors.Join(errs...)
}

type movedFile struct {
	from string
	to   string
}

type strayMove struct {
	backupDir string
	files     []movedFile
}

// restore puts back only files moved by this apply. Existing disabled backups
// from earlier successful applies are never touched.
func (m *strayMove) restore() error {
	if m == nil {
		return nil
	}
	var errs []error
	for i := len(m.files) - 1; i >= 0; i-- {
		f := m.files[i]
		if err := os.Rename(f.to, f.from); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", filepath.Base(f.from), err))
		}
	}
	if m.backupDir != "" {
		if err := os.Remove(m.backupDir); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove rollback directory: %w", err))
		}
	}
	return errors.Join(errs...)
}

// moveStrayConfigsAt relocates every *.json other than config.json into a
// unique disabled/apply-* directory. The returned journal can restore exactly
// this operation if a later install or service restart fails.
func moveStrayConfigsAt(configDir string) (*strayMove, error) {
	entries, err := os.ReadDir(configDir)
	if err != nil {
		return nil, err
	}
	moved := &strayMove{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || name == "config.json" {
			continue
		}
		if moved.backupDir == "" {
			disabled := filepath.Join(configDir, "disabled")
			if err := os.MkdirAll(disabled, 0o755); err != nil {
				return nil, err
			}
			moved.backupDir, err = os.MkdirTemp(disabled, "apply-")
			if err != nil {
				return nil, err
			}
		}
		from := filepath.Join(configDir, name)
		to := filepath.Join(moved.backupDir, name)
		if err := os.Rename(from, to); err != nil {
			restoreErr := moved.restore()
			if restoreErr != nil {
				return nil, fmt.Errorf("move %s into disabled directory: %w; restore moved files: %v", name, err, restoreErr)
			}
			return nil, fmt.Errorf("move %s into disabled directory: %w", name, err)
		}
		moved.files = append(moved.files, movedFile{from: from, to: to})
	}
	return moved, nil
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".singpanel-copy-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, srcFile); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

// copyFileOnce atomically publishes dst without ever replacing an existing
// original backup. A failed or partial copy remains under a temporary name and
// can never make the permanent recovery file look valid.
func copyFileOnce(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := dst + ".new"
	_ = os.Remove(tmp)
	if err := copyFile(src, tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, dst); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

// DetectVersionInstalled returns an error if the official binary is absent.
func DetectVersionInstalled() (string, error) {
	if _, err := os.Stat(SingboxBinary); err != nil {
		return "", err
	}
	return SingboxBinary, nil
}
