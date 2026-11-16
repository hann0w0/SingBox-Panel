package panel

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

type ipLocation struct {
	IP           string `json:"ip"`
	Status       string `json:"status"`
	Country      string `json:"country,omitempty"`
	CountryCode  string `json:"country_code,omitempty"`
	Region       string `json:"region,omitempty"`
	City         string `json:"city,omitempty"`
	Organization string `json:"organization,omitempty"`
	ASN          uint64 `json:"asn,omitempty"`
}

type cachedIPLocation struct {
	value   ipLocation
	expires time.Time
}

type ipLocationService struct {
	mu      sync.Mutex
	cache   map[string]cachedIPLocation
	pending map[string]chan struct{}
	slots   chan struct{}
	client  *http.Client
}

func newIPLocationService() *ipLocationService {
	return &ipLocationService{
		cache: make(map[string]cachedIPLocation), pending: make(map[string]chan struct{}), slots: make(chan struct{}, 4),
		client: &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

var geoExcludedV6 = []netip.Prefix{
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
}

func publicLocationIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if isSpecialUseSubscriptionIP(net.IP(ip.AsSlice())) {
		return false
	}
	if ip.Is6() {
		if !netip.MustParsePrefix("2000::/3").Contains(ip) {
			return false
		}
		for _, prefix := range geoExcludedV6 {
			if prefix.Contains(ip) {
				return false
			}
		}
	}
	return true
}

func (s *ipLocationService) lookup(ctx context.Context, address netip.Addr) ipLocation {
	ip := address.Unmap().String()
	if !publicLocationIP(address) {
		return ipLocation{IP: ip, Status: "private"}
	}
	failure := ipLocation{IP: ip, Status: "error"}
	s.mu.Lock()
	if item, ok := s.cache[ip]; ok && time.Now().Before(item.expires) {
		s.mu.Unlock()
		return item.value
	}
	done := s.pending[ip]
	if done == nil {
		select {
		case s.slots <- struct{}{}:
		default:
			s.mu.Unlock()
			return failure
		}
		done = make(chan struct{})
		s.pending[ip] = done
		go func() {
			// Shared work outlives an individual closed dialog, but is bounded by
			// both a deadline and a global concurrency cap. Never run on delivery.
			value := func() (v ipLocation) {
				v = ipLocation{IP: ip, Status: "error"}
				defer func() {
					if r := recover(); r != nil {
						log.Printf("ip_location: panic fetching %s: %v", ip, r)
					}
				}()
				return s.fetch(ip)
			}()
			ttl := 24 * time.Hour
			if value.Status == "error" {
				ttl = 30 * time.Second
			}
			s.mu.Lock()
			if len(s.cache) >= 1024 {
				var oldest string
				var expiry time.Time
				for key, entry := range s.cache {
					if oldest == "" || entry.expires.Before(expiry) {
						oldest, expiry = key, entry.expires
					}
				}
				delete(s.cache, oldest)
			}
			s.cache[ip] = cachedIPLocation{value: value, expires: time.Now().Add(ttl)}
			delete(s.pending, ip)
			<-s.slots
			close(done)
			s.mu.Unlock()
		}()
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return failure
	case <-done:
		s.mu.Lock()
		defer s.mu.Unlock()
		if item, ok := s.cache[ip]; ok {
			return item.value
		}
		return failure
	}
}

func cleanGeoText(value string) string {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "nil", "null", "unknown":
		return ""
	}
	return truncateRunes(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value), 160)
}

func (s *ipLocationService) fetch(ip string) ipLocation {
	failure := ipLocation{IP: ip, Status: "error"}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://get.geojs.io/v1/ip/geo/"+url.PathEscape(ip)+".json", nil)
	if err != nil {
		return failure
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return failure
	}
	defer resp.Body.Close()
	const maxBody = 16 << 10
	if resp.StatusCode != http.StatusOK || resp.ContentLength > maxBody {
		return failure
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return failure
	}
	var raw struct {
		IP               string `json:"ip"`
		Country          string `json:"country"`
		CountryCode      string `json:"country_code"`
		Region           string `json:"region"`
		City             string `json:"city"`
		Organization     string `json:"organization"`
		OrganizationName string `json:"organization_name"`
		ASN              uint64 `json:"asn"`
	}
	if json.Unmarshal(body, &raw) != nil {
		return failure
	}
	returned, err := netip.ParseAddr(raw.IP)
	if err != nil || returned.Unmap().String() != ip {
		return failure
	}
	value := ipLocation{IP: ip, Status: "ok", Country: cleanGeoText(raw.Country), Region: cleanGeoText(raw.Region), City: cleanGeoText(raw.City), Organization: cleanGeoText(raw.OrganizationName), ASN: raw.ASN}
	code := strings.ToUpper(raw.CountryCode)
	if len(code) == 2 && code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z' {
		value.CountryCode = code
	}
	if value.Organization == "" {
		value.Organization = cleanGeoText(raw.Organization)
	}
	if value.Country == "" && value.CountryCode == "" && value.Region == "" && value.City == "" {
		value.Status = "unknown"
	}
	return value
}

func (a *App) userSubscriptionIPLocation(c *gin.Context) {
	userID, ok := uintParam(c, "id")
	if !ok {
		return
	}
	raw := c.Query("ip")
	address, err := netip.ParseAddr(raw)
	if err != nil || address.Zone() != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid IP address"})
		return
	}
	var row model.SubscriptionFetch
	now := time.Now()
	if err := a.db.WithContext(c.Request.Context()).Select("id").Where("user_id = ? AND ip IN ? AND created_at >= ? AND created_at <= ?", userID, []string{raw, address.Unmap().String()}, now.Add(-subscriptionFetchRetention), now).First(&row).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "subscription IP not found"})
		return
	}
	a.ipLocationsOnce.Do(func() {
		if a.ipLocations == nil {
			a.ipLocations = newIPLocationService()
		}
	})
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, a.ipLocations.lookup(c.Request.Context(), address))
}
