# SingBox Panel

集中管理多台服务器上官方 sing-box 的 Web 面板。可在其中管理节点、入站、出站、路由、用户与订阅，并远程执行 sing-box 的安装、更新、启停、配置下发和卸载。

## 安装

### 原生面板安装

需要 Linux x86_64 或 arm64、systemd、已解析到面板服务器的域名，以及支持 WebSocket 的 HTTPS 反向代理。

```bash
curl -fsSL https://github.com/hann0w0/SingBox-Panel/releases/latest/download/install.sh | sudo bash
```

安装脚本支持非交互安装（`--base-url`、`--admin`、`--password`、`--non-interactive`），用 `--version vX.Y.Z` 可指定版本。安装完成后在面板中创建服务器，并在目标 VPS 上执行该节点详情页生成的 Agent 安装命令。

节点 Agent 支持 Linux amd64/arm64 的 systemd 节点，也支持 Alpine Linux 的 OpenRC。Alpine 节点可直接执行面板生成的命令；脚本会使用 `wget`/`curl` 获取安装包，并为 Agent、sing-box 服务及面板日志读取配置 OpenRC。

### Docker 部署

Docker 仅运行控制面板，各节点 VPS 上的 Agent 和 sing-box 仍以原生方式运行。提供以下两种部署方式（二选一），直接拉取官方镜像即可使用。

前置要求：Docker Engine、已解析到服务器的域名，以及支持 WebSocket 的 HTTPS 反向代理（Compose 方式另需 Compose 插件）。

#### 方式一：docker run

替换域名、管理员账号和初始密码后执行：

```bash
docker run -d \
  --name singbox-panel \
  --restart unless-stopped \
  -p 127.0.0.1:32334:8080 \
  -v singbox-panel-data:/app/data \
  -e SINGBOX_PANEL_BASE_URL='https://panel.example.com' \
  -e SINGBOX_PANEL_ADMIN_EMAIL='admin' \
  -e SINGBOX_PANEL_ADMIN_PASSWORD='请替换为自己的初始密码' \
  ghcr.io/hann0w0/singbox-panel:latest
```

- `SINGBOX_PANEL_BASE_URL`：面板对外公开访问的 HTTPS 根地址（节点 Agent 连接及订阅分发均依赖此地址）。
- 管理员账号和初始密码仅在首次初始化数据库时使用。
- 宿主机仅发布 `127.0.0.1:32334`，映射到容器内 `8080`，需由反向代理统一对外提供 HTTPS。
- 反向代理必须用真实来源地址覆盖 `X-Real-IP`（Nginx：`proxy_set_header X-Real-IP $remote_addr;`），否则登录限流可被伪造的请求头绕过，详见 [Docker 部署说明](docs/docker.md#https-与-websocket-反向代理)。

#### 方式二：Docker Compose

在新目录创建 `compose.yaml`：

```yaml
services:
  panel:
    image: ghcr.io/hann0w0/singbox-panel:latest
    restart: unless-stopped
    ports:
      - "127.0.0.1:32334:8080"
    environment:
      SINGBOX_PANEL_BASE_URL: "https://panel.example.com"
      SINGBOX_PANEL_ADMIN_EMAIL: "admin"
      SINGBOX_PANEL_ADMIN_PASSWORD: "请替换为自己的初始密码"
    volumes:
      - panel-data:/app/data

volumes:
  panel-data:
```

> **提示**：如果初始密码中含有字符 `$`，在 YAML 中需写为 `$$` 转义。

启动服务：

```bash
docker compose up -d
```

更多关于镜像更新、数据备份恢复及生产安全加固配置（如只读文件系统、权限收紧等），详见 [Docker 部署说明](docs/docker.md)。

## 卸载

### 原生面板卸载

```bash
# 卸载面板服务，保留配置与数据库
curl -fsSL https://github.com/hann0w0/SingBox-Panel/releases/latest/download/install.sh | sudo bash -s -- --uninstall --yes

# 完整卸载，连同配置与数据库一并删除
curl -fsSL https://github.com/hann0w0/SingBox-Panel/releases/latest/download/install.sh | sudo bash -s -- --uninstall --purge --yes
```

### Docker 面板卸载

停止并删除容器（数据保存在数据卷中，默认保留）：

```bash
# docker run 方式
docker stop singbox-panel && docker rm singbox-panel

# Docker Compose 方式（在 compose.yaml 目录下）
docker compose down
```

> **注意**：请勿添加 `-v` 或删除数据卷，除非确认清空所有数据。各 VPS 上的节点 Agent 与 sing-box 可在面板对应服务器详情页单独卸载。

## 工作模式

面板通过 HTTPS 提供管理界面，节点上的 Agent 主动以 WSS 连接面板，节点无需开放额外管理端口，也不提供任意 Shell 执行能力。Agent 以官方方式安装 sing-box，下发配置前执行 `sing-box check`，程序与配置更新采用校验、原子替换与失败回滚。systemd 节点从 journal 读取 sing-box 日志；Alpine/OpenRC 节点的服务输出写入 `/var/log/sing-box.log`，Agent 日志写入 `/var/log/singbox-panel-agent.log`，应纳入节点现有的日志轮转策略。

面板支持两种配置模式：管理模式根据面板中的节点、协议与路由设置生成配置；原始配置模式保存并下发完整 JSON，适合面板暂未结构化支持的 sing-box 配置项。

### Reality 前置 SNI 过滤

在 VLESS 入站选择 REALITY 与 TCP 传输后，可以开启「前置 SNI 过滤」。原入站端口仍是客户端和订阅使用的公网端口；另填一个未占用的内部端口，面板自动生成公网 `direct` 入站与仅监听 `127.0.0.1` 的 VLESS 入站。例如公网 `31735`、内部 `31736`，订阅始终连接 `31735`。

SNI 白名单留空时沿用 Reality 的 SNI／握手域名，可填写多个完整域名；名单必须包含订阅使用的域名。嗅探超时留空为 `300ms`，Reality 时间差限制可填写 `1m` 等时长，留空不启用时间差检查。SNI 过滤后仍由 Reality 和 VLESS 完成认证。

面板将过滤规则放在普通分流规则之前，只允许指定前置入站访问对应的本地端口，其余回环保护继续生效。内部端口不能与公网端口、其他入站或统计服务保留端口 `29091`／`29092` 冲突。关闭开关后自动恢复单入站，公网端口和客户端凭证保留。

导入支持明确的单用户组合：一个公网 `direct` 转发至本地 VLESS Reality，开头依次为 TLS 嗅探、SNI 白名单放行、拒绝三条规则。识别后恢复为一个面板入站并使用公网订阅端口；未知字段、目标歧义或更复杂的组合继续保留原始配置模式。

可选真实连接回归：设置 `SING_BOX_BINARY` 为 sing-box 可执行文件的绝对路径，再运行 `go test ./internal/domain/singbox -run TestRealityGuardLive -v`。测试使用本地 TLS 伪装站点与 HTTP 目标，验证允许和拒绝路径及回环保护。

## 用户与订阅

管理员在「用户」列表点击任意用户行，进入该用户的详情页；自己的订阅也从自己的用户行进入，不再单独切换「我的订阅」页签。

- 「已分配」展示该用户的节点，可拖拽左侧手柄排序（支持鼠标、触屏和触控笔，键盘支持上下方向键），填写节点显示名称或取消分配。手机使用紧凑列表，右侧仅保留铅笔与取消分配按钮；点铅笔才展开重命名输入框，点「完成」或回车收起并保留草稿，Esc 取消当前这次改名，名称留空恢复默认值。分配视图支持整行勾选，保存区吸附在底部并避让安全区域。
- 「面板节点」「外部节点」支持按节点、按组勾选分配；切换视图保留当前草稿。
- 点击「保存更改」统一保存分配、顺序和名称，客户端更新订阅后生效；名称仅对当前用户生效，不修改服务器或其他用户的节点名称。离开未保存的页面会提示确认。
- 页面中的 Surge、ClashMeta、Shadowrocket 复制按钮返回当前所选用户的订阅地址，不会切换管理员身份。订阅地址包含访问凭证，请仅交给对应用户。
- 编辑用户与使用记录入口移至详情页；普通用户仍使用原订阅首页，无法读取管理员用户详情接口。
- 「重置链接」同时更换当前用户的订阅 token 和多用户节点连接凭证（UUID/password），不修改登录密码、其他用户或单用户／外部节点的共享凭证。旧订阅地址立即失效，旧节点配置在节点成功同步后失效；离线节点在重连后同步，重置后请更新客户端订阅。

「使用记录」中的订阅拉取按来源 IP 合并计数，支持 24 小时、7 天、30 天范围、筛选和排序，展开后分页查看明细。统计覆盖所选范围的完整记录，不受明细分页条数限制。

管理员查看记录时，面板后端按需向 GeoJS 查询公网 IP 的大致位置和网络归属，仅发送待查询的 IP，不发送用户名、订阅凭证或客户端信息；内网与保留地址不会发送。结果在进程内缓存 24 小时，查询失败不会阻塞订阅下载或隐藏拉取记录。位置不是精确定位，数据保留期为 30 天。

## 导入出站

在「服务器 → 出站 → 新增出站」中，可手动填写，或解析一条节点 URI。解析后确认协议、地址和端口，填写出站标签并保存；出站会按现有配置下发流程应用到服务器。

URI 入口接收单条节点分享链接，解析结果保存为独立的出站配置。订阅地址由外部节点页面管理。

## 流量周期

在服务器新增或编辑中设置每月流量重置日（1–31 日，默认 1 日）。每个周期从该日 **UTC 00:00** 开始，到下次重置时刻结束；不足该日期的月份使用月末，例如 31 日在二月按 28 日或闰年 29 日起算。

流量页保留最近 1 小时、12 小时、24 小时、7 天和 30 天的切换，默认 24 小时，上传、下载、合计及入站明细随所选范围变化。时间统一使用 UTC；各范围只计入最近一次重置之后的流量，重置之前的图表时段显示为零。修改重置日会立即重新划分计量起点，不删除历史记录，也不重启 sing-box；统计范围为面板已采集的 sing-box 流量。

新版 Agent 在控制连接断开期间继续按 UTC 分钟采集；重连后按原时间补报，面板在历史记录与接收序号一并提交后确认，重传不重复计量。此修复需要同步更新节点 Agent；旧 Agent 仍走兼容的累计计数路径。已经错计的历史没有原采集时间，无法自动重建。Agent 进程停止期间无法采集，未确认用量目前保存在 Agent 内存中。

入站重命名后，旧标签的延迟上报仍归属于原入站；历史标签永久保留在该服务器内，不按重命名时间过期。同一入站可以改回自己的旧标签，但其他入站不能复用这些标签（包括删除原入站后新建、导入或编辑原始配置），请为新入站选择新标签，以免离线积压流量串账。删除入站不会把尚未接收的端口或用户用量转移给其他入站；节点总量仍正常接收。升级只能记录当时存在及之后使用的标签，无法恢复升级前已丢失的标签关联。

## 迁移选项

SQLite 数据库升级默认在迁移前生成一致性备份。明确无需备份的部署可临时设置环境变量 `SINGBOX_PANEL_SKIP_MIGRATION_BACKUP=true`，或在配置的 `database` 下设置 `skip_migration_backup: true`。
