package agent

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var publicIPv4Endpoints = []string{
	"https://api4.ipify.org",
	"https://ipv4.icanhazip.com",
}

// SysInfo is a snapshot of host metrics reported to the panel.
type SysInfo struct {
	Hostname string
	OS       string
	Arch     string
	Kernel   string
	Load1    float64
	MemUsed  uint64
	MemTotal uint64
	Uptime   int64
}

// CollectSysInfo gathers host metrics (Linux /proc; degrades gracefully elsewhere).
func CollectSysInfo(ctx context.Context) SysInfo {
	si := SysInfo{Arch: runtime.GOARCH}
	si.Hostname, _ = os.Hostname()
	si.OS = osPrettyName()
	if out, err := run(ctx, "uname", "-r"); err == nil {
		si.Kernel = strings.TrimSpace(out)
	}
	si.Load1 = loadAvg1()
	si.MemUsed, si.MemTotal = memInfo()
	si.Uptime = uptimeSeconds()
	return si
}

// DetectPublicIPv4 asks IPv4-only endpoints for the host's public address.
// The WebSocket itself may prefer IPv6, so its observed source address is not
// a reliable way to populate the panel's default IPv4 display.
func DetectPublicIPv4(ctx context.Context) string {
	return detectPublicIPv4From(ctx, publicIPv4Endpoints)
}

func detectPublicIPv4From(ctx context.Context, endpoints []string) string {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, "tcp4", address)
			if err != nil {
				return dialer.DialContext(ctx, network, address)
			}
			return conn, nil
		},
		TLSHandshakeTimeout: 2 * time.Second,
		ForceAttemptHTTP2:   true,
		DisableKeepAlives:   true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}

	for _, endpoint := range endpoints {
		reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
		if err != nil {
			cancel()
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		cancel()
		if readErr != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
			continue
		}
		addr, err := netip.ParseAddr(strings.TrimSpace(string(raw)))
		if err == nil && addr.Is4() {
			return addr.String()
		}
	}
	return ""
}

func osPrettyName() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return runtime.GOOS
}

func loadAvg1() float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[0], 64)
	return v
}

func memInfo() (used, total uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	var memTotal, memAvail uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			memTotal, _ = strconv.ParseUint(fields[1], 10, 64)
		case "MemAvailable:":
			memAvail, _ = strconv.ParseUint(fields[1], 10, 64)
		}
	}
	// values are in kB.
	memTotal *= 1024
	memAvail *= 1024
	if memAvail > memTotal {
		memAvail = memTotal
	}
	return memTotal - memAvail, memTotal
}

func uptimeSeconds() int64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[0], 64)
	return int64(v)
}
