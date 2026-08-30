package panel

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

const (
	remoteSubscriptionTimeout      = 15 * time.Second
	remoteSubscriptionMaxRedirects = 3
)

// singleRemoteSubscriptionURL accepts only a single HTTP(S) URL. URLs found
// inside pasted or downloaded subscription content are deliberately never
// followed.
func singleRemoteSubscriptionURL(source string) (*url.URL, bool) {
	if strings.ContainsAny(source, "\r\n") {
		return nil, false
	}
	u, err := url.ParseRequestURI(strings.TrimSpace(source))
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, false
	}
	return u, true
}

// fetchRemoteSubscription downloads a subscription while preventing local or
// private network access, including DNS rebinding between validation and dial.
func fetchRemoteSubscription(parent context.Context, u *url.URL) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, remoteSubscriptionTimeout)
	defer cancel()
	if err := validateRemoteSubscriptionURL(ctx, u); err != nil {
		return nil, err
	}

	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialPublicSubscriptionAddress,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		IdleConnTimeout:       15 * time.Second,
		DisableKeepAlives:     true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   remoteSubscriptionTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > remoteSubscriptionMaxRedirects {
				return errors.New("订阅地址重定向次数过多")
			}
			return validateRemoteSubscriptionURL(req.Context(), req.URL)
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("订阅地址无效: %w", err)
	}
	req.Header.Set("Accept", "text/yaml, application/yaml, text/plain, application/octet-stream;q=0.9, */*;q=0.1")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", "SingBox-Panel/1 subscription-import")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("获取订阅失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("订阅服务器返回 HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > singbox.SubscriptionImportMaxBytes {
		return nil, fmt.Errorf("订阅响应超过 %d 字节限制", singbox.SubscriptionImportMaxBytes)
	}

	reader := io.LimitReader(resp.Body, singbox.SubscriptionImportMaxBytes+1)
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return nil, fmt.Errorf("解压订阅响应失败: %w", err)
		}
		defer gz.Close()
		reader = io.LimitReader(gz, singbox.SubscriptionImportMaxBytes+1)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("读取订阅响应失败: %w", err)
	}
	if len(body) > singbox.SubscriptionImportMaxBytes {
		return nil, fmt.Errorf("订阅响应超过 %d 字节限制", singbox.SubscriptionImportMaxBytes)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, errors.New("订阅服务器返回空内容")
	}
	return body, nil
}

func validateRemoteSubscriptionURL(ctx context.Context, u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("仅支持 HTTP(S) 订阅地址")
	}
	if u.User != nil {
		return errors.New("订阅地址不能包含 URL 用户名或密码")
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return errors.New("订阅地址端口无效")
		}
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return errors.New("订阅地址指向本机或内网，已拒绝")
	}
	ips, err := resolveSubscriptionHost(ctx, host)
	if err != nil {
		return fmt.Errorf("解析订阅服务器失败: %w", err)
	}
	return requirePublicSubscriptionIPs(ips)
}

func resolveSubscriptionHost(ctx context.Context, host string) ([]net.IP, error) {
	if zoneAt := strings.LastIndexByte(host, '%'); zoneAt >= 0 {
		host = host[:zoneAt]
	}
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		ips = append(ips, addr.IP)
	}
	if len(ips) == 0 {
		return nil, errors.New("域名没有 IP 地址")
	}
	return ips, nil
}

func requirePublicSubscriptionIPs(ips []net.IP) error {
	if len(ips) == 0 {
		return errors.New("订阅地址没有可用 IP")
	}
	for _, ip := range ips {
		if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
			ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
			isSpecialUseSubscriptionIP(ip) {
			return errors.New("订阅地址指向本机或内网，已拒绝")
		}
	}
	return nil
}

func isSpecialUseSubscriptionIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	for _, prefix := range []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("2001:db8::/32"),
	} {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// dialPublicSubscriptionAddress resolves and validates the destination again
// immediately before opening the socket. This closes the DNS-rebinding gap
// between initial URL validation and http.Transport's connection attempt.
func dialPublicSubscriptionAddress(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("订阅服务器地址无效: %w", err)
	}
	ips, err := resolveSubscriptionHost(ctx, host)
	if err != nil {
		return nil, err
	}
	if err := requirePublicSubscriptionIPs(ips); err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: 8 * time.Second, KeepAlive: -1}
	var lastErr error
	for _, ip := range ips {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}
