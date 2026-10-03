package panel

import (
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

// subscriptionFetchRetention bounds the per-fetch history table.
const subscriptionFetchRetention = 30 * 24 * time.Hour

// subscriptionClientPatterns maps User-Agent fragments to display names.
// Order matters: more specific names come before generic ones.
var subscriptionClientPatterns = []struct{ needle, name string }{
	{"shadowrocket", "Shadowrocket"},
	{"quantumult", "Quantumult X"},
	{"loon", "Loon"},
	{"stash", "Stash"},
	{"surge", "Surge"},
	{"clash-verge", "Clash Verge"},
	{"clashx", "ClashX"},
	{"flclash", "FlClash"},
	{"mihomo", "mihomo"},
	{"clash.meta", "Clash Meta"},
	{"clash-meta", "Clash Meta"},
	{"clashmeta", "Clash Meta"},
	{"clash", "Clash"},
	{"hiddify", "Hiddify"},
	{"sfa", "sing-box"},
	{"sfi", "sing-box"},
	{"sfm", "sing-box"},
	{"sing-box", "sing-box"},
	{"singbox", "sing-box"},
	{"v2rayng", "v2rayNG"},
	{"v2rayn", "v2rayN"},
	{"nekobox", "NekoBox"},
	{"karing", "Karing"},
	{"streisand", "Streisand"},
	{"mozilla", "浏览器"},
	{"curl", "curl"},
	{"wget", "wget"},
}

// subscriptionClient names the client from its User-Agent.
func subscriptionClient(userAgent string) string {
	ua := strings.ToLower(userAgent)
	if strings.TrimSpace(ua) == "" {
		return "未知"
	}
	for _, pattern := range subscriptionClientPatterns {
		if strings.Contains(ua, pattern.needle) {
			return pattern.name
		}
	}
	return "其他"
}

func truncateRunes(value string, max int) string {
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max])
}

// recordSubscriptionFetch stores one subscription download. Failures are
// logged only: usage tracking must never break subscription delivery.
func (a *App) recordSubscriptionFetch(c *gin.Context, user *model.User, format string, status int) {
	now := time.Now()
	ip := clientIP(c)
	ua := truncateRunes(strings.TrimSpace(c.GetHeader("User-Agent")), 255)
	client := subscriptionClient(ua)
	fetch := model.SubscriptionFetch{
		UserID: user.ID, IP: truncateRunes(ip, 64), Client: client,
		Format: truncateRunes(format, 16), UserAgent: ua, Status: status, CreatedAt: now,
	}
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&fetch).Error; err != nil {
			return err
		}
		return tx.Model(&model.User{}).Where("id = ?", user.ID).UpdateColumns(map[string]any{
			"last_sub_at":     now,
			"last_sub_ip":     fetch.IP,
			"last_sub_client": client,
			"last_sub_ua":     ua,
			"sub_fetch_count": gorm.Expr("sub_fetch_count + 1"),
		}).Error
	})
	if err != nil {
		log.Printf("usage: record subscription fetch for user %d: %v", user.ID, err)
	}
}

// recordLogin stores the latest successful panel login.
func (a *App) recordLogin(c *gin.Context, user *model.User) {
	now := time.Now()
	if err := a.db.Model(&model.User{}).Where("id = ?", user.ID).UpdateColumns(map[string]any{
		"last_login_at": now,
		"last_login_ip": truncateRunes(clientIP(c), 64),
	}).Error; err != nil {
		log.Printf("usage: record login for user %d: %v", user.ID, err)
		return
	}
	user.LastLoginAt = &now
	user.LastLoginIP = truncateRunes(clientIP(c), 64)
}

func pruneSubscriptionFetches(db *gorm.DB, now time.Time) error {
	return db.Where("created_at < ?", now.Add(-subscriptionFetchRetention)).Delete(&model.SubscriptionFetch{}).Error
}

type subscriptionActivity struct {
	Fetches int64 `json:"fetches"`
	IPs     int64 `json:"ips"`
}

type subscriptionActivityRow struct {
	UserID  uint  `gorm:"column:user_id"`
	Fetches int64 `gorm:"column:fetches"`
	IPs     int64 `gorm:"column:ips"`
}

// subscriptionActivitySince counts fetches and distinct source addresses per
// user. Many distinct addresses in a short window suggests a shared link.
func subscriptionActivitySince(db *gorm.DB, since time.Time) (map[uint]subscriptionActivity, error) {
	var rows []subscriptionActivityRow
	if err := db.Model(&model.SubscriptionFetch{}).
		Select("user_id, COUNT(*) AS fetches, COUNT(DISTINCT ip) AS ips").
		Where("created_at >= ?", since).
		Group("user_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]subscriptionActivity, len(rows))
	for _, row := range rows {
		out[row.UserID] = subscriptionActivity{Fetches: row.Fetches, IPs: row.IPs}
	}
	return out, nil
}

// listUserSubscriptionFetches returns one user's recent subscription fetches
// and a per-address summary.
func (a *App) listUserSubscriptionFetches(c *gin.Context) {
	userID, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var user model.User
	if err := a.db.Select("id").First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	var fetches []model.SubscriptionFetch
	if err := a.db.Where("user_id = ?", userID).Order("created_at DESC, id DESC").Limit(100).Find(&fetches).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	type ipSummary struct {
		IP       string    `json:"ip"`
		Client   string    `json:"client"`
		Fetches  int64     `json:"fetches"`
		LastSeen time.Time `json:"last_seen"`
	}
	var history []model.SubscriptionFetch
	if err := a.db.Select("ip", "client", "created_at").Where("user_id = ?", userID).
		Order("created_at DESC, id DESC").Limit(5000).Find(&history).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	byIP := map[string]*ipSummary{}
	ips := []ipSummary{}
	order := []string{}
	for _, fetch := range history {
		summary := byIP[fetch.IP]
		if summary == nil {
			// History is newest first, so the first row carries the latest client.
			summary = &ipSummary{IP: fetch.IP, Client: fetch.Client, LastSeen: fetch.CreatedAt}
			byIP[fetch.IP] = summary
			order = append(order, fetch.IP)
		}
		summary.Fetches++
	}
	for _, ip := range order {
		ips = append(ips, *byIP[ip])
	}
	c.JSON(http.StatusOK, gin.H{
		"fetches":        fetches,
		"ips":            ips,
		"retention_days": int(subscriptionFetchRetention / (24 * time.Hour)),
	})
}
