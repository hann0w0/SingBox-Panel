package agent

import (
	"encoding/json"
	"os"

	"github.com/hann0w0/singbox-panel/internal/protocol"
)

// ReadConfig reads the official config file and extracts an inbound summary.
// exists is false when no config is present. This lets the panel discover an
// existing sing-box installation and view its parameters before adopting it.
func ReadConfig() (raw []byte, briefs []protocol.InboundBrief, exists bool) {
	data, err := os.ReadFile(ConfigFile)
	if err != nil {
		return nil, nil, false
	}
	return data, parseInboundBriefs(data), true
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
