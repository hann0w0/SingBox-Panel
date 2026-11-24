package agent

import (
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

type collectedTrafficBucket struct {
	protocol.TrafficTimeBucket
	ports map[string]protocol.PortTrafficSnapshot
	users map[userTrafficKey]protocol.UserTrafficSnapshot
}

const maxBufferedTrafficMinutes = 10080 // 7 days of minute buckets

// Callers hold s.mu. Collection runs regardless of panel connectivity.
func (s *trafficSampler) bucket(now time.Time) *collectedTrafficBucket {
	if s.buckets == nil {
		s.buckets = make(map[int64]*collectedTrafficBucket)
	}
	key := now.UTC().Truncate(time.Minute).Unix()
	s.rebaseFutureClockLocked(key)
	b := s.buckets[key]
	if b == nil {
		b = &collectedTrafficBucket{TrafficTimeBucket: protocol.TrafficTimeBucket{Time: key},
			ports: make(map[string]protocol.PortTrafficSnapshot), users: make(map[userTrafficKey]protocol.UserTrafficSnapshot)}
		s.buckets[key] = b
		s.trimExcessBucketsLocked()
	}
	return b
}

// rebaseFutureClockLocked repairs timestamps recorded before NTP stepped a
// fast RTC clock backward. Without this, an unacknowledged report stamped in
// the future is rejected by the panel forever and blocks all subsequent reports.
func (s *trafficSampler) rebaseFutureClockLocked(nowKey int64) {
	if s.report != nil {
		rebased := false
		for i := range s.report.Buckets {
			if s.report.Buckets[i].Time > nowKey {
				s.report.Buckets[i].Time = nowKey
				rebased = true
			}
		}
		if rebased && len(s.report.Buckets) > 1 {
			s.report.Buckets = mergeReportBucketsByTime(s.report.Buckets)
		}
	}
	var futureKeys []int64
	for k := range s.buckets {
		if k > nowKey {
			futureKeys = append(futureKeys, k)
		}
	}
	if len(futureKeys) == 0 {
		return
	}
	dst := s.buckets[nowKey]
	if dst == nil {
		dst = &collectedTrafficBucket{
			TrafficTimeBucket: protocol.TrafficTimeBucket{Time: nowKey},
			ports:             make(map[string]protocol.PortTrafficSnapshot),
			users:             make(map[userTrafficKey]protocol.UserTrafficSnapshot),
		}
		s.buckets[nowKey] = dst
	}
	for _, fk := range futureKeys {
		mergeCollectedTrafficBucket(dst, s.buckets[fk])
		delete(s.buckets, fk)
	}
}

func (s *trafficSampler) trimExcessBucketsLocked() {
	for len(s.buckets) > maxBufferedTrafficMinutes {
		var oldest, secondOldest int64
		hasOldest, hasSecond := false, false
		for k := range s.buckets {
			if !hasOldest || k < oldest {
				if hasOldest {
					secondOldest, hasSecond = oldest, true
				}
				oldest, hasOldest = k, true
			} else if !hasSecond || k < secondOldest {
				secondOldest, hasSecond = k, true
			}
		}
		if !hasOldest || !hasSecond {
			return
		}
		mergeCollectedTrafficBucket(s.buckets[secondOldest], s.buckets[oldest])
		delete(s.buckets, oldest)
	}
}

func mergeCollectedTrafficBucket(dst, src *collectedTrafficBucket) {
	if dst == nil || src == nil {
		return
	}
	dst.Upload += src.Upload
	dst.Download += src.Download
	dst.UploadRate = max(dst.UploadRate, src.UploadRate)
	dst.DownloadRate = max(dst.DownloadRate, src.DownloadRate)
	dst.TCPConnections = max(dst.TCPConnections, src.TCPConnections)
	dst.UDPConnections = max(dst.UDPConnections, src.UDPConnections)
	for tag, p := range src.ports {
		cur := dst.ports[tag]
		cur.Inbound = tag
		cur.Upload += p.Upload
		cur.Download += p.Download
		cur.UploadRate = max(cur.UploadRate, p.UploadRate)
		cur.DownloadRate = max(cur.DownloadRate, p.DownloadRate)
		dst.ports[tag] = cur
	}
	for uk, u := range src.users {
		cur := dst.users[uk]
		cur.Inbound, cur.User = uk.inbound, uk.user
		cur.Upload += u.Upload
		cur.Download += u.Download
		dst.users[uk] = cur
	}
}

func mergeReportBucketsByTime(buckets []protocol.TrafficTimeBucket) []protocol.TrafficTimeBucket {
	byTime := make(map[int64]*collectedTrafficBucket, len(buckets))
	keys := make([]int64, 0, len(buckets))
	for _, b := range buckets {
		dst := byTime[b.Time]
		if dst == nil {
			dst = &collectedTrafficBucket{
				TrafficTimeBucket: protocol.TrafficTimeBucket{Time: b.Time},
				ports:             make(map[string]protocol.PortTrafficSnapshot),
				users:             make(map[userTrafficKey]protocol.UserTrafficSnapshot),
			}
			byTime[b.Time] = dst
			keys = append(keys, b.Time)
		}
		src := &collectedTrafficBucket{
			TrafficTimeBucket: b,
			ports:             make(map[string]protocol.PortTrafficSnapshot, len(b.Ports)),
			users:             make(map[userTrafficKey]protocol.UserTrafficSnapshot, len(b.Users)),
		}
		for _, p := range b.Ports {
			src.ports[p.Inbound] = p
		}
		for _, u := range b.Users {
			src.users[userTrafficKey{inbound: u.Inbound, user: u.User}] = u
		}
		mergeCollectedTrafficBucket(dst, src)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]protocol.TrafficTimeBucket, 0, len(keys))
	for _, k := range keys {
		b := byTime[k]
		item := b.TrafficTimeBucket
		for _, p := range b.ports {
			item.Ports = append(item.Ports, p)
		}
		for _, u := range b.users {
			item.Users = append(item.Users, u)
		}
		sort.Slice(item.Ports, func(i, j int) bool { return item.Ports[i].Inbound < item.Ports[j].Inbound })
		sort.Slice(item.Users, func(i, j int) bool {
			if item.Users[i].Inbound != item.Users[j].Inbound {
				return item.Users[i].Inbound < item.Users[j].Inbound
			}
			return item.Users[i].User < item.Users[j].User
		})
		out = append(out, item)
	}
	return out
}

func (s *trafficSampler) addPortBucket(now time.Time, inbound string, delta [2]uint64) {
	b := s.bucket(now)
	p := b.ports[inbound]
	p.Inbound = inbound
	p.Upload += delta[0]
	p.Download += delta[1]
	b.ports[inbound] = p
}

func (s *trafficSampler) addUserBucket(now time.Time, key userTrafficKey, delta [2]uint64) {
	b := s.bucket(now)
	u := b.users[key]
	u.Inbound, u.User = key.inbound, key.user
	u.Upload += delta[0]
	u.Download += delta[1]
	b.users[key] = u
}

// accountingSnapshot freezes one bounded batch, leaving newly collected
// bytes in separate buckets. Successful socket writes do not drain it: only
// the panel's post-commit receipt can do that, even after a reconnect.
func (s *trafficSampler) accountingSnapshot() *protocol.TrafficSnapshot {
	snapshot := s.snapshot()
	if snapshot == nil {
		snapshot = &protocol.TrafficSnapshot{Unavailable: true}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.report == nil && len(s.buckets) > 0 {
		if s.accountingSession == "" {
			s.accountingSession = uuid.NewString()
		}
		keys := make([]int64, 0, len(s.buckets))
		for key := range s.buckets {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		if len(keys) > 128 {
			keys = keys[:128]
		}
		s.reportSequence++
		s.report = &protocol.TrafficReport{Session: s.accountingSession, Sequence: s.reportSequence}
		entries := 0
		for _, key := range keys {
			b := s.buckets[key]
			out := b.TrafficTimeBucket
			for tag, p := range b.ports {
				if entries >= 4096 {
					break
				}
				out.Ports = append(out.Ports, p)
				delete(b.ports, tag)
				entries++
			}
			for key, u := range b.users {
				if entries >= 4096 {
					break
				}
				out.Users = append(out.Users, u)
				delete(b.users, key)
				entries++
			}
			sort.Slice(out.Ports, func(i, j int) bool { return out.Ports[i].Inbound < out.Ports[j].Inbound })
			sort.Slice(out.Users, func(i, j int) bool {
				if out.Users[i].Inbound != out.Users[j].Inbound {
					return out.Users[i].Inbound < out.Users[j].Inbound
				}
				return out.Users[i].User < out.Users[j].User
			})
			s.report.Buckets = append(s.report.Buckets, out)
			// A very busy minute can span multiple reports. Its node total is
			// emitted once; remaining port/user rows keep the same UTC timestamp.
			b.TrafficTimeBucket = protocol.TrafficTimeBucket{Time: key}
			if len(b.ports) == 0 && len(b.users) == 0 {
				delete(s.buckets, key)
			}
			if entries >= 4096 {
				break
			}
		}
		// These aggregate mirrors are retained for rate-window bookkeeping;
		// the immutable report above now owns all timestamped accounting bytes.
		clear(s.pendingPorts)
		clear(s.pendingUsers)
		s.peakUpload, s.peakDownload = 0, 0
	}
	snapshot.AccountingVersion = 2
	snapshot.Report = s.report
	snapshot.Ports, snapshot.Users = nil, nil
	return snapshot
}

func (s *trafficSampler) acknowledgeReport(ack protocol.TrafficAck) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.report != nil && s.report.Session == ack.Session && s.report.Sequence == ack.Sequence {
		s.report = nil
	}
}
