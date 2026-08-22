# 官方 sing-box 规范（Agent 严格遵循）

> 本文档为**已核实**的官方事实基线（核实日期 2026-08-22，基线：`v1.14.0-beta.17`，来源：sing-box.sagernet.org 配置页、GitHub Releases API、官方安装脚本）。SingBox Panel 的 Agent 与配置生成器**必须**符合本文。凡改动此处，需重新核对官方来源。

## 0. 版本

- **当前 beta：`v1.14.0-beta.17`**（2026-08-17 发布）。
- stable 仍是 `v1.13.14`；本项目明确按 beta 基线生成和校验配置，允许使用 beta 已公开的字段。

## 1. 官方安装（只用官方发布物）

### 1.1 官方安装脚本（推荐，跨发行版）
```bash
# 最新 beta（本项目基线）
curl -fsSL https://sing-box.app/install.sh | sh -s -- --beta
# 指定 beta 版本
curl -fsSL https://sing-box.app/install.sh | sh -s -- --version 1.14.0-beta.17
```
脚本仅接受 `--beta` 与 `--version <ver>` 两个参数；自动检测包管理器安装官方包（dpkg→.deb、dnf→.rpm、pacman→.pkg.tar.zst、apk、opkg）。受 GitHub 限流时可设 `GITHUB_TOKEN` 环境变量。

### 1.2 官方 APT 源（Debian/Ubuntu）
```bash
sudo mkdir -p /etc/apt/keyrings
sudo curl -fsSL https://sing-box.app/gpg.key -o /etc/apt/keyrings/sagernet.asc
sudo chmod a+r /etc/apt/keyrings/sagernet.asc
echo 'Types: deb
URIs: https://deb.sagernet.org/
Suites: *
Components: *
Enabled: yes
Signed-By: /etc/apt/keyrings/sagernet.asc
' | sudo tee /etc/apt/sources.list.d/sagernet.sources
sudo apt-get update
sudo apt-get install sing-box       # 或 sing-box-beta
```

### 1.3 官方 DNF 源（Fedora/RHEL 系）
```bash
# DNF5
sudo dnf config-manager addrepo --from-repofile=https://sing-box.app/sing-box.repo
# DNF4
sudo dnf config-manager --add-repo https://sing-box.app/sing-box.repo
sudo dnf install sing-box           # 或 sing-box-beta
```

## 2. 官方路径约定（由官方 unit + 包结构确认）

| 项目 | 路径 |
|------|------|
| 二进制 | `/usr/bin/sing-box` |
| 配置目录 | `/etc/sing-box/`（服务以 `-C` **加载整个目录并合并**）|
| 主配置文件 | `/etc/sing-box/config.json`（包自带默认，可覆盖）|
| 工作/状态目录 | `/var/lib/sing-box`（由 `StateDirectory=` 创建）|
| 运行用户 | `sing-box`（sysusers 创建）|
| 主服务单元 | `sing-box.service` |
| 实例模板单元 | `sing-box@.service`（用 `/etc/sing-box/%i.json`）|

> ⚠️ **目录合并**：主服务加载 `/etc/sing-box/` 内**所有** `*.json` 并合并。Agent 只管理 `config.json`，并在首次接管时把该目录内其它 `*.json`（如包自带示例）移入 `/etc/sing-box/disabled/`，避免合并冲突。

## 3. 官方 systemd 单元（beta 包结构一致）

`release/config/sing-box.service`：
```ini
[Unit]
Description=sing-box service
Documentation=https://sing-box.sagernet.org
After=network.target nss-lookup.target network-online.target

[Service]
User=sing-box
StateDirectory=sing-box
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE CAP_SYS_PTRACE CAP_DAC_READ_SEARCH
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE CAP_SYS_PTRACE CAP_DAC_READ_SEARCH
ExecStart=/usr/bin/sing-box -D /var/lib/sing-box -C /etc/sing-box run
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=10s
LimitNOFILE=infinity

[Install]
WantedBy=multi-user.target
```
`sing-box@.service` 与之仅 3 行不同：`StateDirectory=sing-box-%i`、`ExecStart=/usr/bin/sing-box -D /var/lib/sing-box-%i -c /etc/sing-box/%i.json run`。

> **SingBox Panel 只使用官方随包安装的 `sing-box.service`，不改写其核心字段。** 服务操作全部经 `systemctl`：`start/stop/restart/reload/enable/disable sing-box`。`reload` = 官方 `ExecReload` 的 `SIGHUP`，sing-box 支持热重载配置。

## 4. 配置校验 / 管理 CLI（源码逐字核实）

- 校验：`sing-box check -C /etc/sing-box`（目录，与服务加载方式一致）或 `sing-box check -c <file>`
- 格式化：`sing-box format -w -C /etc/sing-box`（`-w` 写回，2 空格缩进、去空字段）
- 版本：`sing-box version`
- 生成：
  - `sing-box generate uuid`
  - `sing-box generate rand <length> [--base64|--hex]`（SS2022 密钥：`rand 16|32 --base64`）
  - `sing-box generate reality-keypair`（输出两行 `PrivateKey: <base64url>` / `PublicKey: <base64url>`，X25519）
  - `sing-box generate tls-keypair <server_name> [-m <月数>]`（自签 PEM 私钥+证书）

**Agent 落盘流程（apply_config）**：写临时文件 → `sing-box check -c <tmp>` → 通过则备份旧 `config.json` 并原子替换 → `systemctl restart sing-box`（服务未运行则 `start`）→ 校验文件 SHA-256 与下发内容一致、进程参数加载受管配置、服务启动时间晚于文件更新时间，并持续确认 active/MainPID 稳定 → 失败则回滚旧配置并再次 `restart`，回执错误。这里不使用 `reload`，因为实际版本中 SIGHUP 可能返回成功但仍继续使用旧配置。

## 5. 服务端 inbound 生成规范（beta 1.14 可用字段）

- **vless**：`users[]{name, uuid, flow}`；`flow` 仅 `xtls-rprx-vision` 或空。可选 `tls`/`transport`/`multiplex`。
- **vmess**：`users[]{name, uuid, alterId}`；`alterId=0`（AEAD，推荐），客户端 `security` 支持 `auto` / `none` / `zero` / `aes-128-gcm` / `chacha20-poly1305` / `aes-128-cfb`。
- **trojan**：`users[]{name, password}`；**tls 必填**。可选 `fallback`/`transport`/`multiplex`。
- **shadowsocks**：`method` + `password` 必填；SS2022（`2022-blake3-*`）密钥须 base64 16/32 字节（`generate rand 16|32 --base64`）；多用户 `users[]{name,password}`。
- **hysteria2**：`users[]{name,password}`；**tls 必填**；`up_mbps`/`down_mbps` 与 `ignore_client_bandwidth` 互斥；`obfs{type:"salamander"|"gecko",password}`；Gecko 可选 `min_packet_size`/`max_packet_size`，且均不得小于 512；`masquerade`。
- **snell**：入站支持版本 `5`/`6`，本项目固定使用顶层 `psk` 单凭证，不生成 `users[]` 或 `userkey`；v5 混淆支持 `none`/`http`/`tls`，v6 使用 `mode`。
- **tuic**：`users[]{name,uuid,password}`（uuid 必填）；**tls 必填**；`congestion_control`: cubic(默认)/new_reno/bbr；`auth_timeout` 默认 `3s`；`heartbeat` 默认 `10s`；不建议 `zero_rtt_handshake`（重放风险）。
- **TLS（inbound.tls）**：`{enabled, server_name, certificate_path, key_path}`；或 **REALITY**：`{enabled, server_name, reality:{enabled, handshake:{server, server_port}, private_key, short_id:[…], max_time_difference}}`；`private_key` 用 `generate reality-keypair`；`short_id` 为 0–8 位十六进制。

### 5.1 仍然禁止的写法
1. ❌ 特殊 outbound `{"type":"block"}` / `{"type":"dns"}`（1.13 **已移除**）→ 用 route rule `action:"reject"` / `action:"hijack-dns"`
2. ❌ inbound 内 `sniff`/`sniff_override_destination`/`domain_strategy` 等 legacy（1.13 **已移除**）→ 用 route rule `action:"sniff"` / `action:"resolve"`
3. ❌ route rule 的 destination-override 字段（1.13 **已移除**）
4. ❌ WireGuard outbound（1.13 **已移除**）→ 用 `endpoints`
5. ❌ `geoip`/`geosite` 字段与文件（1.12 **已移除**）→ 用 `rule_set`
6. ❌ TUN `inet4_address`/`inet6_address`（1.12 **已移除**）→ 合并的 `address`
7. ❌ legacy DNS server `address` 格式 → 使用新格式 `{"type":"udp|tls|https|local",…}`

## 6. 流量统计（关键约束）

- **官方发布构建默认不含 `v2ray_api`**（需 build tag `with_v2ray_api` 自编译）。→ **不得依赖 v2ray StatsService 做每用户统计。**
- **`clash_api` 默认包含在官方构建中**，可用于流量归集。

> ⚠️ **SingBox Panel 不使用它**：入站为单凭证，无法按用户归属字节，面板已移除全部流量统计，生成的配置不含 `experimental` 块。以下仅作官方能力参考：
  ```jsonc
  "experimental": {
    "clash_api": {
      "external_controller": "127.0.0.1:9090",
      "secret": "<随机>"
    }
  }
  ```

## 7. 客户端 outbound（订阅生成用，1.14 beta）

- 通用：`server`+`server_port` 必填；`network` 可限。
- vless-out：`uuid` 必填、`flow:"xtls-rprx-vision"`、`packet_encoding`(默认 xudp)。
- vmess-out：`security` 默认 `auto`（也支持 `none` / `zero` / `aes-128-gcm` / `chacha20-poly1305` / `aes-128-cfb`），`alter_id` 默认 `0`。
- trojan-out：`password` 必填。
- ss-out：`method`/`password` 必填；`plugin` 仅 `obfs-local`/`v2ray-plugin`，选项使用字符串字段 `plugin_opts`。
- hysteria2-out：tls 必填；`obfs` 支持 `salamander`/`gecko`，Gecko 可带包大小边界；`up/down_mbps`；端口跳跃 `server_ports:["a:b"]`+`hop_interval`(1.11+)。
- snell-out：sing-box 仅接受 outbound `version` `4`/`6`；面板将服务端 v5 映射为客户端 outbound v4，固定只输出 `psk`，不输出 `userkey`；v4 混淆支持 `none`/`http`/`tls`。
- tuic-out：uuid+tls 必填；`udp_relay_mode`(native/quic)；`zero_rtt_handshake` 与服务端一致；`heartbeat` 默认 `10s`。
- transport ws：`{"type":"ws","path":"","headers":{},"max_early_data":0,"early_data_header_name":""}`。
- transport HTTPUpgrade：`{"type":"httpupgrade","host":"","path":"","headers":{}}`；不能携带 WebSocket 专属 early-data 字段。
- 客户端 TLS：`{enabled, server_name, insecure, alpn, utls:{enabled, fingerprint}, reality:{enabled, public_key, short_id}}`；REALITY 客户端须同开 utls。
