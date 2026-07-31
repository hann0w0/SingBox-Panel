package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDetectPublicIPv4FromRejectsIPv6AndUsesIPv4Fallback(t *testing.T) {
	server6 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("2001:db8::1\n"))
	}))
	defer server6.Close()
	server4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("203.0.113.7\n"))
	}))
	defer server4.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if got := detectPublicIPv4From(ctx, []string{server6.URL, server4.URL}); got != "203.0.113.7" {
		t.Fatalf("public IPv4 = %q", got)
	}
}
