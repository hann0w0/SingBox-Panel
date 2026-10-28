package model

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// InboundTrafficIdentity keeps every tag owned by an inbound. These bindings
// survive rename and deletion because an offline Agent can report an old tag
// indefinitely; another inbound must never inherit its delayed traffic.
type InboundTrafficIdentity struct {
	ID       uint   `gorm:"primaryKey"`
	ServerID uint   `gorm:"not null;uniqueIndex:idx_inbound_traffic_identity,priority:1"`
	Tag      string `gorm:"size:191;not null"`
	// TagKey is the SHA-256 of Tag. Unlike the tag itself, a hexadecimal key has
	// identical uniqueness semantics on case-insensitive MySQL collations.
	TagKey    string `gorm:"size:64;not null;uniqueIndex:idx_inbound_traffic_identity,priority:2"`
	InboundID uint   `gorm:"not null;index"`
	CreatedAt time.Time
}

func (i *InboundTrafficIdentity) SetTag(tag string) {
	i.Tag = tag
	i.TagKey = InboundTrafficTagKey(tag)
}

func InboundTrafficTagKey(tag string) string {
	sum := sha256.Sum256([]byte(tag))
	return fmt.Sprintf("%x", sum)
}
