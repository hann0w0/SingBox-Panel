package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

// ReadConfig reads the official config file and extracts an inbound summary.
// exists is false when no config is present. This lets the panel discover an
// existing sing-box installation and view its parameters before adopting it.
func ReadConfig() (raw []byte, briefs []protocol.InboundBrief, exists bool) {
	data, err := readConfigFile()
	if err != nil {
		return nil, nil, false
	}
	return data, parseInboundBriefs(data), true
}

func readConfigFile() ([]byte, error) {
	f, err := os.Open(ConfigFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxConfigSize {
		return nil, fmt.Errorf("config exceeds %d-byte limit", maxConfigSize)
	}
	return data, nil
}

func parseInboundBriefs(data []byte) []protocol.InboundBrief {
	var cfg struct {
		Inbounds []struct {
			Tag        string `json:"tag"`
			Type       string `json:"type"`
			ListenPort int    `json:"listen_port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	out := make([]protocol.InboundBrief, 0, len(cfg.Inbounds))
	for _, ib := range cfg.Inbounds {
		out = append(out, protocol.InboundBrief{Tag: ib.Tag, Type: ib.Type, ListenPort: ib.ListenPort})
	}
	return out
}
