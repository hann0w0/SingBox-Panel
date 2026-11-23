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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

var publicIPv4Endpoints = []string{
	"https://api4.ipify.org",
	"https://ipv4.icanhazip.com",
}

type publicAddresses struct {
	IPv4 *string
	IPv6 *string
}

func detectPublicAddresses(ctx context.Context) publicAddresses {
	var addresses publicAddresses
	var probes sync.WaitGroup
	probes.Add(2)
	go func() {
		defer probes.Done()
		if address := DetectPublicIPv4(ctx); address != "" {
			addresses.IPv4 = &address
		}
	}()
	go func() {
		defer probes.Done()
		if address, err := detectLocalIPv6(ctx); err == nil {
			addresses.IPv6 = &address
		}
	}()
	probes.Wait()
	return addresses
}

func selectLocalIPv6(candidates []netip.Addr, preferred netip.Addr) string {
	available := make([]netip.Addr, 0, len(candidates))
	for _, address := range candidates {
		if usablePublicAddress(address, true) {
			if address == preferred {
				return address.String()
			}
			available = append(available, address)
		}
	}
	slices.SortFunc(available, func(left, right netip.Addr) int { return left.Compare(right) })
	if len(available) == 0 {
		return ""
	}
	return available[0].String()
}

func detectLocalIPv6(ctx context.Context) (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	var candidates []netip.Addr
	for _, network := range interfaces {
		if network.Flags&net.FlagUp == 0 || network.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := network.Addrs()
		if err != nil {
			return "", err
		}
		for _, value := range addresses {
			if prefix, err := netip.ParsePrefix(value.String()); err == nil {
				candidates = append(candidates, prefix.Addr())
			}
		}
	}
	preferred := netip.Addr{}
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	if connection, err := dialer.DialContext(ctx, "udp6", "[2606:4700:4700::1111]:53"); err == nil {
		if local, ok := connection.LocalAddr().(*net.UDPAddr); ok {
			preferred = local.AddrPort().Addr()
		}
		connection.Close()
	}
	return selectLocalIPv6(candidates, preferred), nil
}

func usablePublicAddress(address netip.Addr, ipv6 bool) bool {
	return address.IsValid() && address.Is6() == ipv6 && !address.Is4In6() && address.IsGlobalUnicast() && !address.IsPrivate() && address.Zone() == ""
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

var (
	staticSysInfoOnce sync.Once
	staticSysHostname string
	staticSysOS       string
	staticSysKernel   string
)

func loadStaticSysInfo(ctx context.Context) (hostname, osName, kernel string) {
	staticSysInfoOnce.Do(func() {
		staticSysHostname, _ = os.Hostname()
		staticSysOS = osPrettyName()
		if raw, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
			staticSysKernel = strings.TrimSpace(string(raw))
		}
		if staticSysKernel == "" {
			if out, err := run(ctx, "uname", "-r"); err == nil {
				staticSysKernel = strings.TrimSpace(out)
			}
		}
	})
	return staticSysHostname, staticSysOS, staticSysKernel
}

// CollectSysInfo gathers host metrics (Linux /proc; degrades gracefully elsewhere).
func CollectSysInfo(ctx context.Context) SysInfo {
	hostname, osName, kernel := loadStaticSysInfo(ctx)
	si := SysInfo{
		Hostname: hostname,
		OS:       osName,
		Arch:     runtime.GOARCH,
		Kernel:   kernel,
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
	return detectPublicAddressFrom(ctx, endpoints, false)
}

func detectPublicAddressFrom(ctx context.Context, endpoints []string, ipv6 bool) string {
	network := "tcp4"
	if ipv6 {
		network = "tcp6"
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _ string, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
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
		if err == nil && usablePublicAddress(addr, ipv6) {
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
