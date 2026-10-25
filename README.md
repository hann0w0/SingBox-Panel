# SingBox Panel

集中管理多台服务器上官方 sing-box 的 Web 面板。可在其中管理节点、入站、出站、路由、用户与订阅，并远程执行 sing-box 的安装、更新、启停、配置下发和卸载。

## 安装

### 原生面板安装

需要 Linux x86_64 或 arm64、systemd、已解析到面板服务器的域名，以及支持 WebSocket 的 HTTPS 反向代理。

```bash
curl -fsSL https://github.com/hann0w0/SingBox-Panel/releases/latest/download/install.sh | sudo bash
```

安装脚本支持非交互安装（`--base-url`、`--admin`、`--password`、`--non-interactive`），用 `--version vX.Y.Z` 可指定版本。安装完成后在面板中创建服务器，并在目标 VPS 上执行该节点详情页生成的 Agent 安装命令。

### Docker 部署

Docker 仅运行控制面板，各节点 VPS 上的 Agent 和 sing-box 仍以原生方式运行。提供以下两种部署方式（二选一），直接拉取官方镜像即可使用。

前置要求：Docker Engine、已解析到服务器的域名，以及支持 WebSocket 的 HTTPS 反向代理（Compose 方式另需 Compose 插件）。

#### 方式一：docker run

替换域名、管理员账号和初始密码后执行：

```bash
docker run -d \
  --name singbox-panel \
  --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v singbox-panel-data:/app/data \
  -e SINGBOX_PANEL_BASE_URL='https://panel.example.com' \
  -e SINGBOX_PANEL_ADMIN_EMAIL='admin' \
  -e SINGBOX_PANEL_ADMIN_PASSWORD='请替换为自己的初始密码' \
  ghcr.io/hann0w0/singbox-panel:latest
```

- `SINGBOX_PANEL_BASE_URL`：面板对外公开访问的 HTTPS 根地址（节点 Agent 连接及订阅分发均依赖此地址）。
- 管理员账号和初始密码仅在首次初始化数据库时使用。
- 服务仅监听本地 `127.0.0.1:8080`，需由反向代理统一对外提供 HTTPS。

#### 方式二：Docker Compose

在新目录创建 `compose.yaml`：

```yaml
services:
  panel:
    image: ghcr.io/hann0w0/singbox-panel:latest
    restart: unless-stopped
    ports:
      - "127.0.0.1:8080:8080"
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

面板通过 HTTPS 提供管理界面，节点上的 Agent 主动以 WSS 连接面板，节点无需开放额外管理端口，也不提供任意 Shell 执行能力。Agent 以官方方式安装 sing-box，下发配置前执行 `sing-box check`，程序与配置更新采用校验、原子替换与失败回滚。

面板支持两种配置模式：管理模式根据面板中的节点、协议与路由设置生成配置；原始配置模式保存并下发完整 JSON，适合面板暂未结构化支持的 sing-box 配置项。

## 导入出站

在「服务器 → 出站 → 新增出站」中，可手动填写，或解析一条节点 URI。解析后确认协议、地址和端口，填写出站标签并保存；出站会按现有配置下发流程应用到服务器。

URI 入口接收单条节点分享链接，解析结果保存为独立的出站配置。订阅地址由外部节点页面管理。

## 流量周期

在服务器新增或编辑中设置每月流量重置日（1–31 日，默认 1 日）。每个周期从该日 **UTC 00:00** 开始，到下次重置时刻结束；不足该日期的月份使用月末，例如 31 日在二月按 28 日或闰年 29 日起算。

流量页保留最近 1 小时、12 小时、24 小时、7 天和 30 天的切换，默认 24 小时，上传、下载、合计及入站明细随所选范围变化。时间统一使用 UTC；各范围只计入最近一次重置之后的流量，重置之前的图表时段显示为零。修改重置日会立即重新划分计量起点，不删除历史记录，也不重启 sing-box；统计范围为面板已采集的 sing-box 流量。

新版 Agent 在控制连接断开期间继续按 UTC 分钟采集；重连后按原时间补报，面板在历史记录与接收序号一并提交后确认，重传不重复计量。此修复需要同步更新节点 Agent；旧 Agent 仍走兼容的累计计数路径。已经错计的历史没有原采集时间，无法自动重建。Agent 进程停止期间无法采集，未确认用量目前保存在 Agent 内存中。

## 迁移选项

SQLite 数据库升级默认在迁移前生成一致性备份。明确无需备份的部署可临时设置环境变量 `SINGBOX_PANEL_SKIP_MIGRATION_BACKUP=true`，或在配置的 `database` 下设置 `skip_migration_backup: true`。
