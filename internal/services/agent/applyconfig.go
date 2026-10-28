package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
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
func ApplyConfig(ctx context.Context, configBytes []byte) (string, error) {
	configBytes, err := prepareConfigBytes(configBytes)
	if err != nil {
		return "", err
	}
	manager, err := currentServiceManager()
	if err != nil {
		return "", err
	}
	bin, err := DetectVersionInstalled()
	if err != nil {
		return "", fmt.Errorf("sing-box not installed at %s", SingboxBinary)
	}
	if err := os.MkdirAll(ConfigDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", ConfigDir, err)
	}
	if err := secureKnownConfigFiles(); err != nil {
		return "", fmt.Errorf("secure existing config files: %w", err)
	}
	// Preserve the pre-panel config once (never overwritten) for recovery.
	if err := preserveOriginal(); err != nil {
		return "", fmt.Errorf("preserve original config: %w", err)
	}
	expectedHash := sha256.Sum256(configBytes)

	// Nothing to do when the node already runs exactly this config: the panel
	// re-pushes on every agent reconnect (i.e. after every panel restart), and
	// applying would restart sing-box — dropping every live connection — for a
	// byte-identical file. Do not trust file equality alone: the running process
	// must also have started after this file was written and explicitly load the
	// managed config path. Otherwise continue below and perform a real restart.
	if existing, err := readConfigFile(); err == nil &&
		bytes.Equal(existing, configBytes) && manager.Active(ctx) {
		if _, err := currentServiceConfigEvidence(ctx, manager, expectedHash); err == nil {
			return "config unchanged; running sing-box verified", nil
		}
	}

	// 1) write temp (extension is .tmp so the -C directory loader ignores it).
	if err := os.WriteFile(tmpConfigFile, configBytes, 0o600); err != nil {
		return "", fmt.Errorf("write temp config: %w", err)
	}

	// 2) validate with the sing-box binary.
	if out, err := run(ctx, bin, "check", "-c", tmpConfigFile); err != nil {
		_ = os.Remove(tmpConfigFile)
		// Surface the actual reason from sing-box (e.g. a missing certificate or
		// an unsupported field) instead of a bare "exit status 1", so the panel
		// toast tells the operator what to fix.
		if detail := checkFailureDetail(out); detail != "" {
			return out, fmt.Errorf("sing-box check failed: %s", detail)
		}
		return out, fmt.Errorf("sing-box check failed: %w", err)
	}
	previousState := serviceState{active: manager.Active(ctx), enabled: manager.Enabled(ctx)}
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
	if err := os.Chmod(ConfigFile, 0o600); err != nil {
		if rollbackErr := rollbackApply(manager, hadOld, moved, previousState); rollbackErr != nil {
			return "", fmt.Errorf("set config permissions: %w; rollback failed: %v", err, rollbackErr)
		}
		return "", fmt.Errorf("set config permissions: %w", err)
	}

	// 5) apply: restart if running, otherwise start without changing whether the
	// service is enabled at boot. Applying a config must not override an
	// operator's deliberate decision to disable sing-box at boot.
	//
	// Always restart, never reload: sing-box's SIGHUP handling does
	// not reliably re-read the config — the command exits 0 while the process
	// keeps serving the OLD config, so a push would silently not take effect.
	// A restart costs a brief connection drop but is guaranteed to apply.
	var applyErr error
	if out, err := manager.Prepare(ctx); err != nil {
		applyErr = fmt.Errorf("prepare %s service: %w: %s", manager.Kind(), err, out)
	}
	if applyErr == nil {
		if gate, ok := manager.(interface{ waitForConfigStart(context.Context) error }); ok {
			if err := gate.waitForConfigStart(ctx); err != nil {
				applyErr = fmt.Errorf("wait for process clock boundary: %w", err)
			}
		}
	}
	if applyErr == nil {
		action := configApplyServiceAction(previousState)
		if out, err := manager.Action(ctx, action); err != nil {
			applyErr = fmt.Errorf("%s %s: %w: %s", manager.Kind(), action, err, out)
		}
	}

	// 6) verify deterministic evidence that the installed config is the one
	// loaded by this service run; roll back if any part cannot be proven.
	if applyErr == nil {
		applyErr = waitServiceStable(ctx, manager, 3*time.Second, expectedHash)
	}
	if applyErr != nil {
		if rollbackErr := rollbackApply(manager, hadOld, moved, previousState); rollbackErr != nil {
			return "", fmt.Errorf("%w; rollback failed: %v", applyErr, rollbackErr)
		}
		return "", applyErr
	}
	return "config applied and running sing-box verified", nil
}

// prepareConfigBytes produces the bytes ApplyConfig hashes, validates and
// installs. Indentation happens here, ahead of all three, so they operate on one
// byte-identical form; doing it later would hash bytes other than the ones that
// reach the disk. Size is checked before and after formatting because indenting
// only ever grows a config, so a payload just under the limit can still exceed it
// and must be rejected before anything is written.
func prepareConfigBytes(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("config is empty")
	}
	if len(raw) > maxConfigSize {
		return nil, fmt.Errorf("config exceeds %d-byte limit", maxConfigSize)
	}
	formatted := indentConfigJSON(raw)
	if len(formatted) > maxConfigSize {
		return nil, fmt.Errorf("indented config exceeds %d-byte limit (input %d bytes, formatted %d bytes)",
			maxConfigSize, len(raw), len(formatted))
	}
	return formatted, nil
}

// indentConfigJSON renders a config as the multi-line JSON an operator expects to
// read over SSH, and is the single definition of the bytes that get hashed,
// validated and installed. json.Indent only reformats: key order, escaped strings
// and number literals are copied verbatim, so a large integer id or an escaped
// password survives exactly as the panel sent it (re-marshalling through any
// would round-trip big integers through float64). Input that is not well-formed
// JSON is returned unchanged: sing-box's own `check` stays the authority on
// whether a config is valid.
func indentConfigJSON(raw []byte) []byte {
	var buf bytes.Buffer
	// Trim first: json.Indent copies trailing whitespace verbatim, so an input that
	// already ends in a newline would otherwise gain another one on every pass and
	// the function would not be idempotent.
	if err := json.Indent(&buf, bytes.TrimRight(raw, " \t\r\n"), "", "  "); err != nil {
		return raw
	}
	// End the file with a newline so plain `cat`, sftp viewers and diffs treat it
	// as a text file. This runs on the incoming bytes every time, so an unchanged
	// config still compares byte-equal and no sing-box restart is triggered.
	buf.WriteByte('\n')
	return buf.Bytes()
}

// rollbackApply restores the previous config and every JSON file moved by this
// apply. It deliberately uses an independent bounded context: the command
// context is commonly cancelled precisely when rollback becomes necessary.
type serviceState struct {
	active  bool
	enabled bool
}

func configApplyServiceAction(previous serviceState) string {
	if previous.active {
		return "restart"
	}
	return "start"
}

type serviceConfigEvidence struct {
	pid       int
	startedAt time.Time
}

func waitServiceStable(ctx context.Context, manager serviceManager, stableFor time.Duration, expectedHash [sha256.Size]byte) error {
	deadline := time.NewTimer(stableFor)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var applied *serviceConfigEvidence
	for {
		current, err := currentServiceConfigEvidence(ctx, manager, expectedHash)
		if err == nil {
			if applied == nil {
				applied = &current
			} else if current.pid != applied.pid || !current.startedAt.Equal(applied.startedAt) {
				return fmt.Errorf("sing-box process changed while verifying config apply (pid %d -> %d)", applied.pid, current.pid)
			}
		}
		select {
		case <-deadline.C:
			// The timer firing is not evidence by itself. Perform one final
			// complete read so a restart at the end of the stability window
			// cannot be reported as success.
			final, err := currentServiceConfigEvidence(ctx, manager, expectedHash)
			if err != nil {
				return err
			}
			if applied == nil {
				return errors.New("sing-box service never became stable during config verification")
			}
			if final.pid != applied.pid || !final.startedAt.Equal(applied.startedAt) {
				return fmt.Errorf("sing-box process changed at the end of config verification (pid %d -> %d)", applied.pid, final.pid)
			}
			return nil
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("verify service after apply: %w", ctx.Err())
		}
	}
}

func currentServiceConfigEvidence(ctx context.Context, manager serviceManager, expectedHash [sha256.Size]byte) (serviceConfigEvidence, error) {
	if !manager.Active(ctx) {
		return serviceConfigEvidence{}, errors.New("sing-box service is not active after apply")
	}
	pid, err := manager.MainPID(ctx)
	if err != nil {
		return serviceConfigEvidence{}, fmt.Errorf("read sing-box main pid after apply: %w", err)
	}
	if pid <= 0 {
		return serviceConfigEvidence{}, errors.New("sing-box service is active but has no main process")
	}
	startedAt, err := manager.StartedAt(ctx, pid)
	if err != nil {
		return serviceConfigEvidence{}, fmt.Errorf("read sing-box start time after apply: %w", err)
	}
	if err := verifyManagedConfigFile(ConfigFile, expectedHash, startedAt); err != nil {
		return serviceConfigEvidence{}, err
	}
	if err := verifyProcessLoadsManagedConfig(pid); err != nil {
		return serviceConfigEvidence{}, err
	}
	return serviceConfigEvidence{pid: pid, startedAt: startedAt}, nil
}

func verifyManagedConfigFile(path string, expectedHash [sha256.Size]byte, serviceStartedAt time.Time) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open applied config: %w", err)
	}
	defer f.Close()
	beforeInfo, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat opened config before hashing: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxConfigSize+1))
	if err != nil {
		return fmt.Errorf("hash applied config: %w", err)
	}
	if n > maxConfigSize {
		return fmt.Errorf("applied config exceeds %d-byte limit", maxConfigSize)
	}
	var actualHash [sha256.Size]byte
	copy(actualHash[:], h.Sum(nil))
	if actualHash != expectedHash {
		return fmt.Errorf("applied config hash mismatch: got %x, want %x", actualHash, expectedHash)
	}
	openedInfo, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat opened config: %w", err)
	}
	if beforeInfo.Size() != n || openedInfo.Size() != n || beforeInfo.ModTime() != openedInfo.ModTime() {
		return errors.New("applied config changed while it was being hashed")
	}
	pathInfo, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat applied config: %w", err)
	}
	if !os.SameFile(openedInfo, pathInfo) || pathInfo.Size() != n || pathInfo.ModTime() != openedInfo.ModTime() {
		return errors.New("applied config changed while it was being verified")
	}
	if serviceStartedAt.IsZero() || !serviceStartedAt.After(pathInfo.ModTime()) {
		return fmt.Errorf("sing-box started at %s, not after config update at %s",
			serviceStartedAt.Format(time.RFC3339Nano), pathInfo.ModTime().Format(time.RFC3339Nano))
	}
	return nil
}

func verifyProcessLoadsManagedConfig(pid int) error {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return fmt.Errorf("read sing-box process arguments: %w", err)
	}
	parts := bytes.Split(raw, []byte{0})
	args := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			args = append(args, string(part))
		}
	}
	if processArgsLoadManagedConfig(args) {
		return nil
	}
	return fmt.Errorf("sing-box process does not load %s or %s", ConfigFile, ConfigDir)
}

func processArgsLoadManagedConfig(args []string) bool {
	for i, arg := range args {
		switch arg {
		case "-c", "--config":
			if i+1 < len(args) && filepath.Clean(args[i+1]) == ConfigFile {
				return true
			}
		case "-C", "--config-directory":
			if i+1 < len(args) && filepath.Clean(args[i+1]) == ConfigDir {
				return true
			}
		}
		for _, prefix := range []string{"-c=", "--config="} {
			if strings.HasPrefix(arg, prefix) && filepath.Clean(strings.TrimPrefix(arg, prefix)) == ConfigFile {
				return true
			}
		}
		for _, prefix := range []string{"-C=", "--config-directory="} {
			if strings.HasPrefix(arg, prefix) && filepath.Clean(strings.TrimPrefix(arg, prefix)) == ConfigDir {
				return true
			}
		}
	}
	return false
}

func rollbackApply(manager serviceManager, hadOld bool, moved *strayMove, previous serviceState) error {
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
	if out, err := manager.SetEnabled(rollbackCtx, previous.enabled); err != nil {
		errs = append(errs, fmt.Errorf("restore service enablement: %w: %s", err, out))
	}
	action := "stop"
	if previous.active {
		action = "restart"
	}
	if out, err := manager.Action(rollbackCtx, action); err != nil {
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
		if err := os.Chmod(to, 0o600); err != nil {
			restoreErr := moved.restore()
			if restoreErr != nil {
				return nil, fmt.Errorf("secure moved config %s: %w; restore moved files: %v", name, err, restoreErr)
			}
			return nil, fmt.Errorf("secure moved config %s: %w", name, err)
		}
	}
	return moved, nil
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".singbox-panel-copy-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, srcFile); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
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

func secureKnownConfigFiles() error {
	for _, path := range []string{ConfigFile, tmpConfigFile, bakConfigFile, origConfigFile} {
		if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("chmod %s: %w", path, err)
		}
	}
	return nil
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

// DetectVersionInstalled returns an error if the sing-box binary is absent.
func DetectVersionInstalled() (string, error) {
	bin := singboxBinary()
	if _, err := os.Stat(bin); err != nil {
		return "", err
	}
	return bin, nil
}

// checkFailureDetail extracts a concise, human-readable reason from the output
// of `sing-box check`. The binary prints the failure (often behind a
// FATAL[NNNN] log prefix); return the most informative line, capped so it fits
// in a UI toast. Returns "" when there is nothing useful to show.
func checkFailureDetail(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	// The actual error is the last non-empty line sing-box prints.
	lines := strings.Split(out, "\n")
	msg := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			msg = s
			break
		}
	}
	// Drop a leading log-level/timestamp prefix like "FATAL[0000] ".
	if i := strings.Index(msg, "] "); i > 0 && i < 16 {
		msg = strings.TrimSpace(msg[i+2:])
	}
	const max = 300
	if len([]rune(msg)) > max {
		msg = string([]rune(msg)[:max]) + "…"
	}
	return msg
}
