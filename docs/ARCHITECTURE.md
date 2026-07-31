# SingPanel 架构设计

## 1. 组件

| 组件 | 语言 | 职责 |
|------|------|------|
| **Panel** | Go | REST API（管理端 + 用户端）、Agent WebSocket 网关、数据库、配置生成与下发、订阅输出 |
| **Agent** | Go | 部署在每台 VPS，反连面板；安装/升级官方 sing-box；写 `/etc/sing-box/config.json`；管理 `sing-box.service`；上报主机状态；读日志；连通性测试；自升级或自卸载 |
| **Web** | React/TS | 管理面板 + 用户面板 |

Panel 与 Agent 在**同一个 Go module**里，共享 `internal/protocol` 与 `internal/singbox`，避免类型漂移。Agent 二进制仅编译 `cmd/agent` 依赖到的包。

## 2. 通信模型：Agent 反向拨号

- Agent 主动向面板建立 **WSS** 长连接（`wss://panel/api/agent/ws`），只需 VPS 出站可达面板，无需入站端口。
- 认证：每台服务器一条 `Server` 记录，携带随机 `agent_token`（bearer）。首次连接 = 注册/认领。
- 面板维护内存中的 **AgentHub**：`server_id -> live conn`。API 层通过 Hub 向指定 Agent 派发指令并等待结果（带 `command_id` 关联的 request/response）。
- 断线：Agent 指数退避重连；面板标记 `offline`（超过心跳超时）。

### 消息封套

```jsonc
{ "type": "install_singbox", "id": "cmd-uuid", "payload": { ... } }
```

- `type`：见 `internal/protocol`。
- `id`：指令关联 ID；Agent 回 `command_result` 时带同一 `id`。
- 事件类（心跳/日志）由 Agent 主动推送，`id` 可空。

**Panel → Agent（指令，白名单）**
- `install_singbox`  {channel: stable|beta, version?: string, method: script|apt|dnf}
- `apply_config`     {config: <sing-box config.json>} → 与磁盘比对（相同则跳过）→ `sing-box check` → 备份 → 原子替换 → `daemon-reload` + `restart` → 校验 active，失败回滚
- `service_action`   {action: start|stop|restart|reload|enable|disable}
- `get_status`
- `get_config`       读取节点现有 config.json（用于「识别并导入」）
- `get_logs`         {lines} → `journalctl -u sing-box`（只读）
- `test_outbound`    {host, port} → 从该节点 TCP 连落地，测可达与延迟
- `test_egress`      {url?} → 该节点到公网的延迟（默认 generate_204）
- `update_agent`     Agent 下载面板当前提供的二进制，原子替换并自重启
- `uninstall_agent`  删除 Agent 二进制、配置文件与 systemd 自启服务；保留 sing-box

**Agent → Panel（事件/回执）**
- `register`         {agent_version, hostname, os, arch, kernel, singbox_installed, singbox_version, panel_url}
- `heartbeat`        {ts, load1, mem_used, mem_total, uptime, singbox_active}
- `command_result`   {id, ok, error?, output?, data?}
- `log`              {level, msg}

> Agent 永不接受“执行任意命令”指令。新增能力 = 新增受控指令类型。

## 3. 数据模型（GORM）

```
User        面板用户（管理员/普通用户）。含 ServerIDs / InboundIDs 授权与 ExpireAt。
Server      一台 VPS（= 一个 Agent）。含 agent_token、在线状态、系统信息、sing-box 版本、FinalOutbound、ConfigMode、RawConfig、AgentURL。
Inbound     某台 Server 上的一个协议入站（type/port/settings）。settings 以 JSON 存储。
Outbound    某台 Server 上的出站（落地 / 中转目标）。
RouteRule   某台 Server 上的分流规则：匹配条件（含 inbound 标签）→ 目标出站。
RuleSet     远程规则集（geoip/geosite 风格），供 RouteRule 引用。
Setting     KV 配置。
```

关系：
- `Server 1..* Inbound / Outbound / RouteRule / RuleSet`
- `User.ServerIDs` 决定可用节点；`User.InboundIDs` 进一步限定到具体协议（为空 = 该节点全部协议）

### 用户授权与「单凭证」

**入站一律单凭证**：一个协议一套固定凭证，写死在配置里，不随面板用户变化。
因此「授权」只影响**订阅内容**——用户订阅里只会出现被授权的协议（例如家宽节点可只留给自己）。

单凭证的线格式**因协议而异**，由 `internal/singbox/inbound.go` 决定：
- `users` 数组恰好一条：vless / vmess / trojan / hysteria2 / tuic / anytls
- 顶层凭证、无 `users`：shadowsocks（`method`+`password`）、snell（`psk`）

### 没有流量记账

单凭证下代理层无法把字节归属到具体用户，因此面板**不做流量统计**，也不生成 `experimental.clash_api`。
用户的启停只由 `ExpireAt` 决定（`reconciler` 定时停用过期用户）。

## 4. 订阅

- 每个用户一个 `sub_token`，订阅地址：`https://panel/api/sub/{token}`。
- 根据 `User-Agent` / `?target=` 返回：
  - `sing-box` — 原生 outbound JSON（含 selector/urltest）
  - `clash` / `clash-meta` — **仅 `proxies:` 数组**，规则与策略组由用户自己的配置决定
  - 默认 — **URI 链接逐行输出**（不做 base64）
  - `surge` — **仅 `[Proxy]` 段**
- 内容 = 用户被授权的节点 × 被授权的协议。节点地址取 `Server.Address`，为空则回退到 Agent 上报的公网 IP。
- snell **没有通用的分享链接格式**，不会出现在 URI 订阅里；用户端「详情」会展示完整参数供手动添加。

## 5. 配置生成与导入

- **面板管理模式**：`internal/singbox` 依据 Inbound/Outbound/RouteRule 记录生成整份官方 `config.json`，键序固定为
  `log → inbounds → outbounds → route`（用有序结构体，而非 map）。不生成 dns 与 experimental 块。
  生成后由 Agent `sing-box check` 校验再落盘。
- **原始配置模式**：管理员直接编辑或导入的完整 JSON 存入 `Server.RawConfig`，重连时原样下发，不丢弃 DNS、experimental、selector 等结构化表单不认识的字段。
- **导入**：`internal/singbox/parse.go` 同时生成结构化预览，并把完整原文保存为事实源。导入不立即改动服务器文件；管理员显式切回面板管理模式后，才会用结构化记录覆盖原始配置。
- 所有配置写入按服务器串行化；原始配置下发失败、或切换面板管理失败时，数据库模式会回滚。

## 6. 部署

- Panel：Docker 容器（多阶段构建：前端 → Go → alpine），默认 SQLite。外层反代 + TLS。
- Agent：交叉编译的单二进制，安装脚本注册为 `singpanel-agent.service`（与官方 `sing-box.service` 分离，互不影响）。

## 7. 安全

- Panel↔Agent：TLS + per-agent bearer token。
- 指令白名单；`apply_config` 前 `sing-box check`，失败不落盘、保留旧配置；首次接管前的原配置永久保留为 `config.json.orig`。
- 面板 API：JWT；管理端与用户端角色隔离；密码 bcrypt；每次请求重新检查账号状态和令牌版本，修改密码或停用账号会撤销旧会话。
- Agent 以 root 运行，仅做受控操作；日志留痕（`command_result` 回执 + 面板审计）。
