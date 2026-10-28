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

// User usage is stored hourly by the panel. Bound the identity-discovery
// window even for long-lived anonymous connections and nodes without the
// sing-box user statistics API.
const maxUnassignedTrafficHours = 24

// ledgerEntry is the accounting state of one sing-box connection. Both the
// Clash poll and the API connection stream report cumulative per-connection
// totals for the same connection ids; the ledger counts only the part of an
// observed total that has not been counted yet, so whichever source sees a
// byte first counts it and the other never counts it again.
type ledgerEntry struct {
	inbound           string
	user              string
	guardOuter        bool
	inboundType       string
	guardClassified   bool
	unclassifiedTotal [2]uint64
	nodeObserved      bool
	nodeCounted       [2]uint64
	// counted is the highest observed total; baseline is the part of it that
	// predates the first successful node sample and is never attributed.
	counted  [2]uint64
	baseline [2]uint64
	// userCounted is the attributed part already credited to user or excluded
	// after its identity-discovery window expired. A user that becomes known
	// later is credited only the remaining, timestamped part once.
	userCounted [2]uint64
	firstSeen   time.Time
	unassigned  map[int64][2]uint64
}

type userTrafficKey struct {
	inbound string
	user    string
}

func (entry *ledgerEntry) pruneUnassigned(now time.Time) {
	cutoff := now.UTC().Truncate(time.Hour).Add(-(maxUnassignedTrafficHours - 1) * time.Hour).Unix()
	discard := func(hour int64) {
		value := entry.unassigned[hour]
		entry.userCounted[0] += value[0]
		entry.userCounted[1] += value[1]
		delete(entry.unassigned, hour)
	}
	for hour := range entry.unassigned {
		if hour < cutoff {
			discard(hour)
		}
	}
	// The age cutoff alone is insufficient if the source clock moved backwards.
	for len(entry.unassigned) > maxUnassignedTrafficHours {
		var oldest int64
		first := true
		for hour := range entry.unassigned {
			if first || hour < oldest {
				oldest, first = hour, false
			}
		}
		discard(oldest)
	}
	if len(entry.unassigned) == 0 {
		entry.unassigned = nil
	}
}

// observeConnection records a cumulative total for one connection and
// attributes the uncounted part to its inbound and (when known) its user.
// baseline marks a connection first seen with history that predates the
// accounting window. Callers hold s.mu.
func (s *trafficSampler) observeConnection(id, inbound, user string, total [2]uint64, baseline bool, now time.Time) {
	s.observeConnectionFrom(id, inbound, "", user, total, baseline, time.Time{}, now, nil)
}

func (s *trafficSampler) observeConnectionFrom(id, inbound, inboundType, user string, total [2]uint64, baseline bool, createdAt, now time.Time, guards map[string]bool) {
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
	if entry.inboundType == "" {
		entry.inboundType = inboundType
	}
	if !entry.guardClassified && entry.inbound != "" && entry.inboundType != "" && (entry.inboundType != "direct" || guards != nil) {
		entry.guardOuter = entry.inboundType == "direct" && guards[entry.inbound]
		entry.guardClassified = true
	}
	if entry.user == "" {
		entry.user = user
	}
	for i := range total {
		total[i] = max(total[i], entry.unclassifiedTotal[i])
	}
	if entry.inboundType == "direct" && !entry.guardClassified && s.haveSample {
		// A source without a captured layout cannot decide whether this is a
		// redundant outer leg. Wait for a classified cumulative observation.
		entry.unclassifiedTotal = total
		return
	}
	entry.unclassifiedTotal = [2]uint64{}
	var delta [2]uint64
	for i := range total {
		if total[i] > entry.counted[i] {
			delta[i] = total[i] - entry.counted[i]
			entry.counted[i] = total[i]
			if !s.haveSample {
				// The stream can start before Clash, or update an entry first seen
				// by an earlier stream event. Keep advancing its baseline until the
				// node counter baseline exists; none of these bytes may be billed.
				entry.baseline[i] += delta[i]
			}
		}
	}
	if entry.inbound != "" {
		s.observeNodeTraffic(entry, createdAt, now)
	}
	if !s.haveSample {
		return
	}
	if entry.inbound == "" {
		return
	}
	if entry.guardOuter {
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
		s.addPortBucket(now, entry.inbound, delta)
	}
	if entry.user == "" {
		if delta[0] != 0 || delta[1] != 0 {
			if entry.unassigned == nil {
				entry.unassigned = make(map[int64][2]uint64)
			}
			key := now.UTC().Truncate(time.Hour).Unix()
			v := entry.unassigned[key]
			v[0] += delta[0]
			v[1] += delta[1]
			entry.unassigned[key] = v
		}
		entry.pruneUnassigned(now)
		return
	}
	entry.pruneUnassigned(now)
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
	// A user may become known after Clash already counted its bytes. Attribute
	// those bytes to their original collection buckets, not the discovery time.
	remaining := userDelta
	for bucket, value := range entry.unassigned {
		credit := [2]uint64{min(value[0], remaining[0]), min(value[1], remaining[1])}
		s.addUserBucket(time.Unix(bucket, 0), key, credit)
		remaining[0] -= credit[0]
		remaining[1] -= credit[1]
	}
	entry.unassigned = nil
	if remaining[0] != 0 || remaining[1] != 0 {
		s.addUserBucket(now, key, remaining)
	}
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
