package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

// Reuse the client renderer and config importer so credentials (including
// SS2022 composite passwords), TLS and transport use the same wire format as
// subscriptions. Only client settings are copied into the new outbound.
func outboundFromClientNode(cn singbox.ClientNode) (outboundReq, error) {
	if !supportedOutboundTypes[cn.Type] || cn.Type == "direct" {
		return outboundReq{}, fmt.Errorf("该协议暂不支持用作出站：%s", cn.Type)
	}
	name := cn.Name
	cn.Name = "imported-outbound"
	raw, err := singbox.BuildClientOutbound(cn)
	if err != nil {
		return outboundReq{}, err
	}
	config, err := json.Marshal(map[string]any{"outbounds": []json.RawMessage{raw}})
	if err != nil {
		return outboundReq{}, err
	}
	parsed, err := singbox.ParseServerConfig(config)
	if err != nil || len(parsed.Outbounds) != 1 {
		return outboundReq{}, fmt.Errorf("无法转换为受支持的出站配置")
	}
	o := parsed.Outbounds[0]
	o.Settings.PacketEncoding = cn.Settings.PacketEncoding
	settings, err := json.Marshal(outboundStoredSettings{
		Server: o.Server, ServerPort: o.ServerPort, UUID: o.UUID,
		Username: o.Username, Password: o.Password, Settings: o.Settings,
	})
	return outboundReq{Tag: name, Type: o.Type, Settings: settings}, err
}

func (a *App) availableOutboundTag(serverID uint, name string) (string, error) {
	base := strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(name))
	if base == "" {
		base = "imported-outbound"
	}
	var tags []string
	if err := a.db.Model(&model.Outbound{}).Where("server_id = ?", serverID).Pluck("tag", &tags).Error; err != nil {
		return "", err
	}
	used := map[string]bool{"direct": true, "block": true, "reject": true, "sniff": true, "hijack-dns": true}
	for _, tag := range tags {
		used[tag] = true
	}
	tag := base
	for i := 2; used[tag]; i++ {
		tag = fmt.Sprintf("%s-%d", base, i)
	}
	return tag, nil
}

// Preview is read-only. Saving uses the normal createOutbound validation and
// config-apply path. Three sources are accepted:
//
//   - a panel node (source_kind=inbound) or an external node
//     (source_kind=custom_node), which produce an outbound that keeps following
//     that node;
//   - a share link (uri), which produces an independent snapshot as before.
func (a *App) previewOutboundImport(c *gin.Context) {
	sid, ok := uintParam(c, "id")
	if !ok || !a.requireManagedMode(c, sid) {
		return
	}
	c.Header("Cache-Control", "no-store")
	var req struct {
		URI        string `json:"uri"`
		SourceKind string `json:"source_kind"`
		SourceID   uint   `json:"source_id"`
	}
	if !bindJSON(c, &req) {
		return
	}
	req.URI = strings.TrimSpace(req.URI)
	if req.SourceKind != "" || req.SourceID != 0 {
		kind, err := normalizeSourceKind(req.SourceKind)
		if err == nil && req.SourceID == 0 {
			err = fmt.Errorf("请选择一个节点")
		}
		var body outboundReq
		if err == nil {
			body, err = a.outboundSourceBody(sid, kind, req.SourceID)
		}
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"outbound": body, "source_kind": kind, "source_id": req.SourceID})
		return
	}
	if req.URI == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写一条节点 URI"})
		return
	}
	if len(req.URI) > 65536 || strings.ContainsAny(req.URI, "\r\n") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请一次填写一条节点 URI（最多 64 KB）"})
		return
	}
	cn, err := singbox.ParseShareLink(req.URI)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "链接解析失败：" + err.Error()})
		return
	}
	body, err := outboundFromClientNode(cn)
	if err == nil {
		body.Tag, err = a.availableOutboundTag(sid, body.Tag)
	}
	if err == nil {
		err = a.validateOutbound(a.db, sid, 0, body.Tag, body.Type, body.Settings)
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"outbound": body})
}
