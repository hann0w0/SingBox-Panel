package panel

import (
	"compress/gzip"
	"context"
	"encoding/json"
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

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/model"
	"github.com/hann0w0/singbox-panel/internal/singbox"
)

const (
	customNodeImportTimeout      = 15 * time.Second
	customNodeImportMaxRedirects = 3
)

// customNodeImportReq accepts either one pasted source (share links, encoded
// list, Clash YAML, Surge profile, or an HTTP(S) subscription URL), or a links
// array supplied by API clients. Link is retained as a compatibility alias for
// the old single-link form. Audience fields are intentionally absent: imported
// nodes start unassigned and are granted only from the user access picker.
type customNodeImportReq struct {
	Source    string   `json:"source"`
	Links     []string `json:"links"`
	Link      string   `json:"link"`
	Group     string   `json:"group"`
	Enabled   *bool    `json:"enabled"`
	SortOrder *int     `json:"sort_order"`
}

type customNodeImportResponse struct {
	Nodes      any                   `json:"nodes"`
	Items      any                   `json:"items,omitempty"`
	Skipped    []singbox.ImportIssue `json:"skipped"`
	SourceType string                `json:"source_type"`
	Count      int                   `json:"count"`
	Fetched    bool                  `json:"fetched,omitempty"`
}

func (a *App) previewCustomNodeImport(c *gin.Context) {
	var req customNodeImportReq
	if !bindJSON(c, &req) {
		return
	}
	result, fetched, err := parseCustomNodeImportRequest(c.Request.Context(), req)
	if err != nil {
		writeCustomNodeImportError(c, result, fetched, err)
		return
	}
	// Items is a compatibility alias used by early clients of the preview API.
	// The current web UI consumes Nodes.
	c.JSON(http.StatusOK, customNodeImportResponse{
		Nodes: result.Nodes, Items: result.Nodes, Skipped: nonNilImportIssues(result.Skipped),
		SourceType: result.SourceType, Count: len(result.Nodes), Fetched: fetched,
	})
}

func (a *App) importCustomNodes(c *gin.Context) {
	var req customNodeImportReq
	if !bindJSON(c, &req) {
		return
	}
	result, fetched, err := parseCustomNodeImportRequest(c.Request.Context(), req)
	if err != nil {
		writeCustomNodeImportError(c, result, fetched, err)
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	baseSort := 0
	if req.SortOrder != nil {
		baseSort = *req.SortOrder
	}
	group := trimRunes(strings.TrimSpace(req.Group), 64)
	rows := make([]model.CustomNode, 0, len(result.Nodes))
	for i := range result.Nodes {
		item := result.Nodes[i]
		params, marshalErr := json.Marshal(item.Params)
		if marshalErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "节点参数无法保存: " + marshalErr.Error()})
			return
		}
		link := strings.TrimSpace(item.Link)
		// CustomNode.Link is intentionally capped at 1024. Long VMess or plugin
		// URIs still import losslessly through the already-parsed structured form.
		if len(link) > 1024 {
			link = ""
		}
		validation := customNodeReq{
			Name: item.Name, Link: link, Protocol: item.Protocol,
			Address: item.Address, Port: item.Port, Params: params,
		}
		if _, validateErr := validateCustomNode(&validation); validateErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("节点 %q 无法保存: %v", item.Name, validateErr)})
			return
		}
		rows = append(rows, model.CustomNode{
			// Imported nodes are deliberately not published to any account. The
			// user-access modal is the only place where assignment is changed.
			AllUsers: false, UserIDs: []uint{}, ExcludedUserIDs: []uint{},
			Name: trimRunes(strings.TrimSpace(item.Name), 128), Group: group, Link: validation.Link,
			Protocol: validation.Protocol, Address: validation.Address, Port: validation.Port,
			Params: model.JSONText(params), Enabled: enabled, SortOrder: baseSort + i,
		})
	}
	if err := a.db.Transaction(func(tx *gorm.DB) error {
		if len(rows) == 0 {
			return errors.New("没有可导入节点")
		}
		return tx.Create(&rows).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "批量保存节点失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, customNodeImportResponse{
		Nodes: rows, Skipped: nonNilImportIssues(result.Skipped), SourceType: result.SourceType,
		Count: len(rows), Fetched: fetched,
	})
}

func parseCustomNodeImportRequest(ctx context.Context, req customNodeImportReq) (singbox.SubscriptionParseResult, bool, error) {
	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = strings.TrimSpace(req.Link)
	}
	if len(req.Links) > 0 {
		parts := make([]string, 0, len(req.Links)+1)
		if source != "" {
			parts = append(parts, source)
		}
		for _, link := range req.Links {
			if link = strings.TrimSpace(link); link != "" {
				parts = append(parts, link)
			}
		}
		source = strings.Join(parts, "\n")
	}
	if source == "" {
		return singbox.SubscriptionParseResult{}, false, errors.New("请输入分享链接或订阅内容")
	}
	if len(source) > singbox.SubscriptionImportMaxBytes {
		return singbox.SubscriptionParseResult{}, false, fmt.Errorf("订阅内容超过 %d 字节限制", singbox.SubscriptionImportMaxBytes)
	}

	fetched := false
	raw := []byte(source)
	// A remote subscription is recognized only when the entire source is one
	// HTTP(S) URL. URLs found inside fetched or pasted content are never followed.
	if u, ok := singleRemoteSubscriptionURL(source); ok {
		var err error
		raw, err = fetchRemoteSubscription(ctx, u)
		if err != nil {
			return singbox.SubscriptionParseResult{}, false, err
		}
		fetched = true
	}
	result, err := singbox.ParseSubscription(raw)
	if err != nil {
		return result, fetched, err
	}
	if len(result.Nodes) == 0 {
		return result, fetched, errors.New("没有找到可导入节点")
	}
	return result, fetched, nil
}

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

func writeCustomNodeImportError(c *gin.Context, result singbox.SubscriptionParseResult, fetched bool, err error) {
	c.JSON(http.StatusBadRequest, gin.H{
		"error": err.Error(), "nodes": nonNilImportedNodes(result.Nodes),
		"items": nonNilImportedNodes(result.Nodes), "skipped": nonNilImportIssues(result.Skipped),
		"source_type": result.SourceType, "count": len(result.Nodes), "fetched": fetched,
	})
}

func nonNilImportedNodes(nodes []singbox.ImportedNode) []singbox.ImportedNode {
	if nodes == nil {
		return []singbox.ImportedNode{}
	}
	return nodes
}

func nonNilImportIssues(issues []singbox.ImportIssue) []singbox.ImportIssue {
	if issues == nil {
		return []singbox.ImportIssue{}
	}
	return issues
}

func trimRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

func fetchRemoteSubscription(parent context.Context, u *url.URL) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, customNodeImportTimeout)
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
		Timeout:   customNodeImportTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > customNodeImportMaxRedirects {
				return errors.New("订阅地址重定向次数过多")
			}
			if err := validateRemoteSubscriptionURL(req.Context(), req.URL); err != nil {
				return err
			}
			return nil
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
	var reader io.Reader = io.LimitReader(resp.Body, singbox.SubscriptionImportMaxBytes+1)
	// Many providers gzip subscription payloads regardless of Accept-Encoding.
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
	if err := requirePublicSubscriptionIPs(ips); err != nil {
		return err
	}
	return nil
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
		netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
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
