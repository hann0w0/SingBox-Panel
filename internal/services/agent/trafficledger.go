package agent

import (
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

// maxTrafficTombstones bounds remembered closed connections. sing-box keeps
// its last 1000 closed connections and replays them on every new stream
// subscription, so the agent must remember at least that many to avoid
// counting a replayed connection twice.
const maxTrafficTombstones = 4096

// ledgerEntry is the accounting state of one sing-box connection. Both the
// Clash poll and the API connection stream report cumulative per-connection
// totals for the same connection ids; the ledger counts only the part of an
// observed total that has not been counted yet, so whichever source sees a
// byte first counts it and the other never counts it again.
type ledgerEntry struct {
	inbound string
	user    string
	// counted is the highest observed total; baseline is the part of it that
	// predates the agent and is never attributed.
	counted  [2]uint64
	baseline [2]uint64
	// userCounted is the attributed part already credited to user. A user that
	// becomes known after the first bytes were counted is credited the
	// remainder once.
	userCounted [2]uint64
	firstSeen   time.Time
}

type userTrafficKey struct {
	inbound string
	user    string
}

// observeConnection records a cumulative total for one connection and
// attributes the uncounted part to its inbound and (when known) its user.
// baseline marks a connection first seen with history that predates the
// accounting window. Callers hold s.mu.
func (s *trafficSampler) observeConnection(id, inbound, user string, total [2]uint64, baseline bool, now time.Time) {
	entry := s.ledger[id]
	if entry == nil {
		entry = s.tombstones[id]
	}
	if entry == nil {
		entry = &ledgerEntry{firstSeen: now}
		if baseline {
			entry.counted = total
			entry.baseline = total
		}
		s.ledger[id] = entry
	}
	if entry.inbound == "" {
		entry.inbound = inbound
	}
	if entry.user == "" {
		entry.user = user
	}
	var delta [2]uint64
	for i := range total {
		if total[i] > entry.counted[i] {
			delta[i] = total[i] - entry.counted[i]
			entry.counted[i] = total[i]
		}
	}
	if entry.inbound == "" {
		return
	}
	if delta[0] > 0 || delta[1] > 0 {
		window := s.portWindow[entry.inbound]
		window[0] += delta[0]
		window[1] += delta[1]
		s.portWindow[entry.inbound] = window
		port := s.pendingPorts[entry.inbound]
		port.Inbound = entry.inbound
		port.Upload += delta[0]
		port.Download += delta[1]
		s.pendingPorts[entry.inbound] = port
	}
	if entry.user == "" {
		return
	}
	var userDelta [2]uint64
	for i := range userDelta {
		attributed := entry.counted[i] - entry.baseline[i]
		if attributed > entry.userCounted[i] {
			userDelta[i] = attributed - entry.userCounted[i]
			entry.userCounted[i] = attributed
		}
	}
	if userDelta[0] == 0 && userDelta[1] == 0 {
		return
	}
	key := userTrafficKey{inbound: entry.inbound, user: entry.user}
	pending := s.pendingUsers[key]
	pending.Inbound = entry.inbound
	pending.User = entry.user
	pending.Upload += userDelta[0]
	pending.Download += userDelta[1]
	s.pendingUsers[key] = pending
}

// closeConnection moves an active entry to the bounded tombstone set, so a
// later final total (stream CLOSED, or a closed connection replayed by a new
// subscription) only adds what was not yet counted. Callers hold s.mu.
func (s *trafficSampler) closeConnection(id string) {
	entry := s.ledger[id]
	if entry == nil {
		return
	}
	delete(s.ledger, id)
	if _, exists := s.tombstones[id]; !exists {
		s.tombstoneOrder = append(s.tombstoneOrder, id)
	}
	s.tombstones[id] = entry
	for len(s.tombstoneOrder) > maxTrafficTombstones {
		delete(s.tombstones, s.tombstoneOrder[0])
		s.tombstoneOrder = s.tombstoneOrder[1:]
	}
}

// closeMissing closes active entries absent from a complete view of active
// connections taken at viewStartedAt. Entries first seen after that moment
// may be newer than the view and are kept. Callers hold s.mu.
func (s *trafficSampler) closeMissing(active map[string]bool, viewStartedAt time.Time) {
	for id, entry := range s.ledger {
		if !active[id] && entry.firstSeen.Before(viewStartedAt) {
			s.closeConnection(id)
		}
	}
}

// pendingUser returns the unsent delta for one inbound user (tests).
func (s *trafficSampler) pendingUser(inbound, user string) protocol.UserTrafficSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingUsers[userTrafficKey{inbound: inbound, user: user}]
}
