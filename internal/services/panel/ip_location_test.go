package panel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

type geoTransportFunc func(*http.Request) (*http.Response, error)

func (f geoTransportFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
func geoResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestIPLocationSkipsNonPublicAddresses(t *testing.T) {
	s := newIPLocationService()
	s.client.Transport = geoTransportFunc(func(*http.Request) (*http.Response, error) { panic("private address sent upstream") })
	for _, raw := range []string{"127.0.0.1", "10.1.1.1", "172.16.1.1", "192.168.1.1", "100.64.0.1", "169.254.169.254", "0.1.1.1", "203.0.113.1", "224.0.0.1", "::1", "fc00::1", "fe80::1", "fec0::1", "2001:db8::1", "64:ff9b::a00:1", "::ffff:192.168.1.1"} {
		if value := s.lookup(context.Background(), netip.MustParseAddr(raw)); value.Status != "private" {
			t.Fatalf("%s: %+v", raw, value)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicLocationIP(netip.MustParseAddr(raw)) {
			t.Fatalf("public IP rejected: %s", raw)
		}
	}
}

func TestIPLocationCoalescesAndCaches(t *testing.T) {
	s := newIPLocationService()
	var calls atomic.Int32
	s.client.Transport = geoTransportFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.URL.String() != "https://get.geojs.io/v1/ip/geo/1.1.1.1.json" || req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
			t.Errorf("unexpected upstream request: %s", req.URL)
		}
		return geoResponse(`{"ip":"1.1.1.1","country_code":"au","country":"Australia","region":"nil","organization_name":"Cloudflare","asn":13335}`), nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if result := s.lookup(context.Background(), netip.MustParseAddr("1.1.1.1")); result.CountryCode != "AU" || result.Status != "ok" || result.Region != "" {
				t.Errorf("unexpected location: %+v", result)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("upstream requests: %d", calls.Load())
	}
}

func TestIPLocationInvalidResponsesAndUnknownLocation(t *testing.T) {
	for _, body := range []string{`bad`, `{"ip":"8.8.8.8","country":"wrong"}`, strings.Repeat("x", 16385)} {
		s := newIPLocationService()
		s.client.Transport = geoTransportFunc(func(*http.Request) (*http.Response, error) { return geoResponse(body), nil })
		if got := s.lookup(context.Background(), netip.MustParseAddr("1.1.1.1")); got.Status != "error" {
			t.Fatalf("invalid body accepted: %+v", got)
		}
	}
	s := newIPLocationService()
	s.client.Transport = geoTransportFunc(func(*http.Request) (*http.Response, error) {
		return geoResponse(`{"ip":"1.1.1.1","region":"nil","city":"unknown","organization_name":"Cloudflare\nInc."}`), nil
	})
	if got := s.lookup(context.Background(), netip.MustParseAddr("1.1.1.1")); got.Status != "unknown" || got.Organization != "CloudflareInc." {
		t.Fatalf("unknown location: %+v", got)
	}
}

func TestIPLocationConcurrencyAndCancellation(t *testing.T) {
	s := newIPLocationService()
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	s.client.Transport = geoTransportFunc(func(req *http.Request) (*http.Response, error) {
		entered <- struct{}{}
		<-release
		return geoResponse(`{"ip":"1.1.1.1"}`), nil
	})
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "8.8.4.4", "9.9.9.9"} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s.lookup(ctx, netip.MustParseAddr(ip))
		<-entered
	}
	if got := s.lookup(context.Background(), netip.MustParseAddr("1.0.0.1")); got.Status != "error" {
		t.Fatal("concurrency cap not enforced")
	}
	once.Do(func() { close(release) })
	if got := s.lookup(context.Background(), netip.MustParseAddr("1.1.1.1")); got.Status != "unknown" {
		t.Fatalf("one cancelled waiter cancelled shared work: %+v", got)
	}
}

func TestIPLocationEndpointOnlyQueriesRetainedUserAddresses(t *testing.T) {
	db := testDB(t)
	app := &App{db: db, ipLocations: newIPLocationService()}
	var calls atomic.Int32
	app.ipLocations.client.Transport = geoTransportFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return geoResponse(`{"ip":"1.1.1.1"}`), nil })
	for _, row := range []model.SubscriptionFetch{
		{UserID: 1, IP: "1.1.1.1", CreatedAt: time.Now()},
		{UserID: 2, IP: "8.8.8.8", CreatedAt: time.Now()},
		{UserID: 1, IP: "9.9.9.9", CreatedAt: time.Now().Add(-31 * 24 * time.Hour)},
	} {
		mustCreate(t, db, &row)
	}
	router := gin.New()
	router.GET("/users/:id/location", app.userSubscriptionIPLocation)
	for ip, want := range map[string]int{"1.1.1.1": 200, "8.8.8.8": 404, "9.9.9.9": 404, "example.com": 400} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/users/1/location?ip=%s", ip), nil))
		if rec.Code != want {
			t.Fatalf("%s: %d %s", ip, rec.Code, rec.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("queries sent for unrecorded IPs: %d", calls.Load())
	}
}

func TestIPLocationRecoversFromPanic(t *testing.T) {
	s := newIPLocationService()
	var panicking atomic.Bool
	panicking.Store(true)
	s.client.Transport = geoTransportFunc(func(*http.Request) (*http.Response, error) {
		if panicking.Load() {
			panic("simulated transport explosion")
		}
		return geoResponse(`{"ip":"8.8.8.8","country_code":"US","country":"United States"}`), nil
	})

	got := s.lookup(context.Background(), netip.MustParseAddr("1.1.1.1"))
	if got.Status != "error" {
		t.Fatalf("expected status error on panic, got: %+v", got)
	}

	// Ensure slot was released and subsequent lookups work
	panicking.Store(false)
	got2 := s.lookup(context.Background(), netip.MustParseAddr("8.8.8.8"))
	if got2.Status != "ok" || got2.CountryCode != "US" {
		t.Fatalf("subsequent lookup failed after panic recovery: %+v", got2)
	}
}
