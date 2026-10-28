package agent

import (
	"encoding/json"
	"time"
)

// Only the generated direct -> loopback VLESS/REALITY chain is redundant.
// A tag suffix alone is not evidence: administrators may name normal inbounds
// freely, and a guard itself may have an entirely custom tag.
func parseRealityGuardInbounds(raw []byte) map[string]bool {
	type inbound struct {
		Type            string `json:"type"`
		Tag             string `json:"tag"`
		Listen          string `json:"listen"`
		ListenPort      int    `json:"listen_port"`
		OverrideAddress string `json:"override_address"`
		OverridePort    int    `json:"override_port"`
		TLS             struct {
			Enabled bool `json:"enabled"`
			Reality struct {
				Enabled bool `json:"enabled"`
			} `json:"reality"`
		} `json:"tls"`
	}
	var cfg struct {
		Inbounds  []json.RawMessage `json:"inbounds"`
		Outbounds []struct {
			Type string `json:"type"`
			Tag  string `json:"tag"`
		} `json:"outbounds"`
		Route struct {
			Rules []json.RawMessage `json:"rules"`
		} `json:"route"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return nil
	}
	// An empty map is a known unguarded layout; nil means no layout was read.
	guards := make(map[string]bool)
	direct := 0
	for _, outbound := range cfg.Outbounds {
		if outbound.Tag == "direct" {
			if outbound.Type != "direct" {
				return guards
			}
			direct++
		}
	}
	if direct != 1 {
		return guards
	}
	inbounds := make(map[string]inbound)
	byPort := make(map[int][]inbound)
	rawInbounds := make(map[string]json.RawMessage)
	for _, raw := range cfg.Inbounds {
		var in inbound
		if json.Unmarshal(raw, &in) != nil || in.Tag == "" {
			return guards
		}
		if _, duplicate := inbounds[in.Tag]; duplicate {
			return guards
		}
		inbounds[in.Tag], rawInbounds[in.Tag] = in, raw
		byPort[in.ListenPort] = append(byPort[in.ListenPort], in)
	}
	for i := 0; i+2 < len(cfg.Route.Rules); i += 3 {
		var sniff struct {
			Inbound []string `json:"inbound"`
			Timeout string   `json:"timeout"`
		}
		if json.Unmarshal(cfg.Route.Rules[i], &sniff) != nil || len(sniff.Inbound) != 1 {
			break
		}
		tag := sniff.Inbound[0]
		gate := inbounds[tag]
		if gate.Type != "direct" || gate.ListenPort < 1 || gate.ListenPort > 65535 ||
			gate.OverridePort < 1 || gate.OverridePort > 65535 || gate.ListenPort == gate.OverridePort ||
			len(byPort[gate.ListenPort]) != 1 || len(byPort[gate.OverridePort]) != 1 {
			break
		}
		target := byPort[gate.OverridePort][0]
		if target.Type != "vless" || target.Listen != "127.0.0.1" || !target.TLS.Enabled || !target.TLS.Reality.Enabled {
			break
		}
		if !trafficJSONEqual(rawInbounds[tag], map[string]any{
			"type": "direct", "tag": tag, "listen": "::", "listen_port": gate.ListenPort, "network": "tcp",
			"override_address": "127.0.0.1", "override_port": gate.OverridePort,
		}) {
			break
		}
		if timeout, err := time.ParseDuration(sniff.Timeout); err != nil || timeout <= 0 ||
			!trafficJSONEqual(cfg.Route.Rules[i], map[string]any{
				"inbound": []string{tag}, "action": "sniff", "sniffer": []string{"tls"}, "timeout": sniff.Timeout,
			}) {
			break
		}
		var allow struct {
			Rules []struct {
				Domain []string `json:"domain"`
			} `json:"rules"`
		}
		if json.Unmarshal(cfg.Route.Rules[i+1], &allow) != nil || len(allow.Rules) != 2 || len(allow.Rules[0].Domain) == 0 ||
			!trafficJSONEqual(cfg.Route.Rules[i+1], map[string]any{
				"type": "logical", "mode": "and", "action": "route", "outbound": "direct",
				"rules": []map[string]any{
					{"inbound": []string{tag}, "domain": allow.Rules[0].Domain},
					{"ip_cidr": []string{"127.0.0.1/32"}, "port": []int{gate.OverridePort}},
				},
			}) || !trafficJSONEqual(cfg.Route.Rules[i+2], map[string]any{"inbound": []string{tag}, "action": "reject", "method": "drop"}) {
			break
		}
		guards[tag] = true
	}
	return guards
}

func trafficJSONEqual(raw []byte, expected any) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	got, err := json.Marshal(value)
	if err != nil {
		return false
	}
	want, err := json.Marshal(expected)
	return err == nil && string(got) == string(want)
}

// Once activated, keep this accounting boundary until the Agent exits. Going
// back to global counters while an old stream drains would count its final
// inner bytes twice. Existing connection classifications stay immutable.
// Only node accounting rebaselines: port/user counters and pending ACK batches
// retain their original boundary, including bytes arriving during the switch.
// Callers hold s.mu.
func (s *trafficSampler) activateConnectionAccounting(guards map[string]bool) {
	if len(guards) == 0 || s.connectionAccounting {
		return
	}
	s.connectionAccounting = true
	s.nodeWindow = [2]uint64{}
}

func (s *trafficSampler) setStatsStreamAvailable(available bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statsStreamAvailable = available
}

// Without the stream's final CLOSED totals, polling alone cannot observe all
// short-lived connections. Do not advertise complete live counters in that
// degraded mode; pending timestamped reports remain available for delivery.
func (s *trafficSampler) trafficAvailable() bool {
	return s.available && s.haveSample && (!s.connectionAccounting || (s.nodeHaveSample && s.statsStreamAvailable))
}

// Node counters get their own high-water mark because the global-to-ledger
// switch must not reset the established per-port or per-user billing window.
// Callers hold s.mu.
func (s *trafficSampler) observeNodeTraffic(entry *ledgerEntry, createdAt, now time.Time) {
	if !s.connectionAccounting {
		return
	}
	if s.nodeHaveSample && !entry.guardClassified {
		return
	}
	if !entry.nodeObserved {
		entry.nodeObserved = true
		if createdAt.IsZero() || createdAt.Before(s.nodeStartedAt) {
			entry.nodeCounted = entry.counted
		}
	}
	var delta [2]uint64
	for i, total := range entry.counted {
		if total > entry.nodeCounted[i] {
			delta[i] = total - entry.nodeCounted[i]
			entry.nodeCounted[i] = total
		}
	}
	if !s.nodeHaveSample || entry.guardOuter {
		return
	}
	bucket := s.bucket(now)
	bucket.Upload += delta[0]
	bucket.Download += delta[1]
	for i := range delta {
		s.nodeCounted[i] += delta[i]
		s.nodeWindow[i] += delta[i]
	}
}

func (s *trafficSampler) nodeTotals() (uint64, uint64) {
	if s.connectionAccounting {
		return s.nodeCounted[0], s.nodeCounted[1]
	}
	return s.lastUpload, s.lastDownload
}
