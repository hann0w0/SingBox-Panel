package panel

import (
	"errors"
	"fmt"
	"log"
	"net/netip"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

var errLocalInbound = errors.New("本机入站不提供公网节点")
var errNodeAddress = errors.New("节点连接地址不可用")

func nodeAddressError(err error) error {
	return fmt.Errorf("%w: %v", errNodeAddress, err)
}

func publicAddress(raw string, ipv6 bool) (string, bool) {
	address, err := netip.ParseAddr(raw)
	if err != nil || address.Is6() != ipv6 || address.Is4In6() || !address.IsGlobalUnicast() || address.IsPrivate() || address.Zone() != "" {
		return "", false
	}
	return address.String(), true
}

func setServerAddressAvailability(server *model.Server) {
	_, server.IPv4Available = publicAddress(server.PublicIP, false)
	_, server.IPv6Available = publicAddress(server.PublicIPv6, true)
}

func inboundNodeAddress(server *model.Server, settings *singbox.InboundSettings) (string, error) {
	listen, err := netip.ParseAddr(settings.ListenAddress())
	if err != nil {
		return "", fmt.Errorf("无效的入站监听地址")
	}
	if listen.IsLoopback() {
		return "", errLocalInbound
	}
	if !listen.IsUnspecified() {
		if host, ok := publicAddress(listen.String(), listen.Is6()); ok {
			preserveNodeTLSName(server.Address, host, settings)
			return host, nil
		}
		return "", fmt.Errorf("入站监听地址不是可导出的公网地址")
	}
	// A configured domain is the dial target for exported/subscribed nodes and
	// takes precedence over the raw public IP, which cannot cover both address
	// families behind one name.
	if domain, err := normalizeNodeAddress(server.Address); err != nil {
		return "", err
	} else if domain != "" && !isLoopbackOrUnspecifiedHost(domain) {
		return domain, nil
	}
	var host string
	if settings.IPv6Only {
		host, _ = publicAddress(server.PublicIPv6, true)
		if host == "" {
			return "", fmt.Errorf("服务器 %q 尚无有效的公网 IPv6，请升级或检查 Agent 与 IPv6 网络", server.Name)
		}
	} else if listen.Is4() {
		host, _ = publicAddress(server.PublicIP, false)
		if host == "" {
			return "", fmt.Errorf("服务器 %q 尚无有效的公网 IPv4，请检查 Agent 或填写 IPv4 地址", server.Name)
		}
	} else {
		host, _ = publicAddress(server.PublicIP, false)
		if host == "" {
			host, _ = publicAddress(server.PublicIPv6, true)
		}
		if host == "" || isLoopbackOrUnspecifiedHost(host) {
			return "", fmt.Errorf("服务器 %q 没有可用的公网连接地址", server.Name)
		}
	}
	return host, nil
}

func preserveNodeTLSName(configured, host string, settings *singbox.InboundSettings) {
	name, err := normalizeNodeAddress(configured)
	if err != nil || name == "" || name == host || settings.TLS.ServerName != "" || settings.TLS.Reality.Enabled || settings.TLS.ACMEDomain != "" {
		return
	}
	if _, err := netip.ParseAddr(name); err != nil {
		settings.TLS.ServerName = name
	}
}

func resolveInboundListen(server *model.Server, settings *singbox.InboundSettings) error {
	if !settings.IPv6Only || settings.ListenAddress() != "::" {
		return nil
	}
	address, ok := publicAddress(server.PublicIPv6, true)
	if !ok {
		return fmt.Errorf("服务器 %q 没有已知的本机公网 IPv6，无法配置 IPv6 监听", server.Name)
	}
	settings.Listen = address
	return nil
}

func (h *Hub) updatePublicAddresses(serverID uint, legacy string, ipv4, ipv6 *string) bool {
	var server model.Server
	if err := h.db.First(&server, serverID).Error; err != nil {
		log.Printf("addresses: load server %d: %v", serverID, err)
		return false
	}
	updates := make(map[string]any)
	changed := false
	for _, report := range []struct {
		value                       *string
		previous, column, timestamp string
		ipv6                        bool
	}{
		{ipv4, server.PublicIP, "public_ip", "public_ipv4_updated_at", false},
		{ipv6, server.PublicIPv6, "public_ipv6", "public_ipv6_updated_at", true},
	} {
		if report.value == nil {
			continue
		}
		address := ""
		if *report.value != "" {
			var ok bool
			address, ok = publicAddress(*report.value, report.ipv6)
			if !ok {
				continue
			}
		}
		updates[report.column] = address
		updates[report.timestamp] = time.Now()
		changed = changed || address != report.previous
	}
	if ipv4 == nil {
		if address, ok := publicAddress(legacy, false); ok {
			updates["public_ip"] = address
			changed = changed || address != server.PublicIP
		}
	}
	if len(updates) == 0 {
		return false
	}
	if err := h.db.Model(&server).Updates(updates).Error; err != nil {
		log.Printf("addresses: update server %d: %v", serverID, err)
		return false
	}
	return changed
}

func (a *App) reconcileNodeAddresses(serverID uint) {
	a.orch.PushConfigIfManaged(serverID)
	a.reconcileNodeConsumers(serverID)
}

func (a *App) reconcileNodeConsumers(serverID uint) {
	var consumers []uint
	err := a.db.Model(&model.Outbound{}).
		Where("source_kind = ? AND source_id IN (?)", model.OutboundSourceInbound, a.db.Model(&model.Inbound{}).Select("id").Where("server_id = ?", serverID)).
		Distinct().Pluck("server_id", &consumers).Error
	if err != nil {
		log.Printf("addresses: load consumers of server %d: %v", serverID, err)
		return
	}
	for _, consumer := range consumers {
		if consumer != serverID && consumer != 0 {
			a.orch.PushConfigIfManaged(consumer)
		}
	}
}
