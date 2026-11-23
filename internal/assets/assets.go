package assets

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io"
	"io/fs"
	"strings"
	"sync"
)

//go:embed all:web
var rawWebFS embed.FS

//go:embed all:agents
var rawAgentsFS embed.FS

var (
	agentChecksumMu sync.Mutex
	agentChecksums  = map[string]string{}
)

// WebFS returns the embedded compiled frontend directory when the binary was
// built with frontend assets staged in internal/assets/web.
func WebFS() (fs.FS, bool) {
	if _, err := fs.Stat(rawWebFS, "web/index.html"); err != nil {
		return nil, false
	}
	sub, err := fs.Sub(rawWebFS, "web")
	if err != nil {
		return nil, false
	}
	return sub, true
}

// AgentsFS returns the embedded agent directory when both Linux agent binaries
// were staged in internal/assets/agents at build time.
func AgentsFS() (fs.FS, bool) {
	if _, err := fs.Stat(rawAgentsFS, "agents/singbox-panel-agent-linux-amd64"); err != nil {
		return nil, false
	}
	if _, err := fs.Stat(rawAgentsFS, "agents/singbox-panel-agent-linux-arm64"); err != nil {
		return nil, false
	}
	sub, err := fs.Sub(rawAgentsFS, "agents")
	if err != nil {
		return nil, false
	}
	return sub, true
}

// AgentVersionFromFS reads the trimmed VERSION file from an agent filesystem.
func AgentVersionFromFS(fsys fs.FS) (string, bool) {
	if fsys == nil {
		return "", false
	}
	data, err := fs.ReadFile(fsys, "VERSION")
	if err != nil {
		return "", false
	}
	v := strings.TrimSpace(string(data))
	if v == "" {
		return "", false
	}
	return v, true
}

// AgentChecksumFromFS returns the hex SHA-256 digest for arch in fsys.
// For the built-in embedded FS, digests are cached for the lifetime of the process.
func AgentChecksumFromFS(fsys fs.FS, arch string, cacheable bool) (string, error) {
	if cacheable {
		agentChecksumMu.Lock()
		sum, ok := agentChecksums[arch]
		agentChecksumMu.Unlock()
		if ok {
			return sum, nil
		}
	}
	name := "singbox-panel-agent-linux-" + arch
	f, err := fsys.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if cacheable {
		agentChecksumMu.Lock()
		agentChecksums[arch] = sum
		agentChecksumMu.Unlock()
	}
	return sum, nil
}
