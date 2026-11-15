package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

const requestedConfigSourceFile = ConfigFile + ".source"

type requestedConfigSource struct {
	InstalledSHA256 string `json:"installed_sha256"`
	Requested       []byte `json:"requested_config"`
}

func applyRequestedConfig(ctx context.Context, command protocol.ApplyConfigCmd, resolve restrictedOutboundResolver, apply func(context.Context, []byte) (string, error), sourcePath string) (string, error) {
	prepared, err := prepareRestrictedOutbounds(ctx, command.Config, command.RestrictedOutboundTags, resolve)
	if err != nil {
		return "", err
	}
	var installed []byte
	if len(command.RestrictedOutboundTags) > 0 {
		installed, err = prepareConfigBytes(prepared)
		if err != nil {
			return "", err
		}
	}
	out, err := apply(ctx, prepared)
	if err != nil {
		return out, err
	}
	if len(command.RestrictedOutboundTags) == 0 {
		if err := os.Remove(sourcePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return out, fmt.Errorf("config applied, but clearing requested config source failed: %w", err)
		}
		return out, nil
	}
	digest := sha256.Sum256(installed)
	source, err := json.Marshal(requestedConfigSource{
		InstalledSHA256: hex.EncodeToString(digest[:]),
		Requested:       command.Config,
	})
	if err == nil {
		err = writeAtomicFile(sourcePath, source, 0o600)
	}
	if err != nil {
		cleanupErr := os.Remove(sourcePath)
		if errors.Is(cleanupErr, os.ErrNotExist) {
			cleanupErr = nil
		}
		return out, fmt.Errorf("config applied, but saving requested config source failed: %w", errors.Join(err, cleanupErr))
	}
	return out, nil
}

func readRequestedConfig(actual []byte) []byte {
	return readRequestedConfigAt(actual, requestedConfigSourceFile)
}

func readRequestedConfigAt(actual []byte, sourcePath string) []byte {
	file, err := os.Open(sourcePath)
	if err != nil {
		return actual
	}
	defer file.Close()
	const maxSourceSize = 2*maxConfigSize + 1024
	raw, err := io.ReadAll(io.LimitReader(file, maxSourceSize+1))
	if err != nil || len(raw) > maxSourceSize {
		return actual
	}
	var source requestedConfigSource
	if json.Unmarshal(raw, &source) != nil || len(source.Requested) == 0 || len(source.Requested) > maxConfigSize || !json.Valid(source.Requested) {
		return actual
	}
	digest := sha256.Sum256(actual)
	if source.InstalledSHA256 != hex.EncodeToString(digest[:]) {
		return actual
	}
	return source.Requested
}
