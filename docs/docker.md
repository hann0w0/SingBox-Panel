# Docker 部署

Docker 只运行管理面板。每个被管理的 VPS 继续使用面板生成的安装命令部署原生 Agent，Agent 通过 WSS 主动连接面板，并由 systemd 管理该 VPS 上的 sing-box。即使节点与面板在同一台机器，也在宿主机上安装 Agent。

## 从源码构建并启动

需要 Docker Engine 和 Docker Compose 插件、可访问的 HTTPS 域名。生产服务器支持 Linux amd64、arm64；构建无需在宿主机安装 Go 或 Node.js。

```bash
git clone https://github.com/hann0w0/SingBox-Panel.git
cd SingBox-Panel
cp .env.example .env
chmod 600 .env
```

编辑 `.env`：

```dotenv
PANEL_BASE_URL=https://panel.example.com
PANEL_ADMIN=admin
PANEL_ADMIN_PASSWORD='这里填写自己的初始密码'
PANEL_IMAGE=singbox-panel:local
PANEL_VERSION=dev
PANEL_BIND=127.0.0.1
PANEL_PORT=8080
```

- `PANEL_BASE_URL` 是对外 HTTPS 地址，不带路径。它用于 Agent 连接、安装命令和订阅链接，不能填容器名或内部地址。
- `PANEL_ADMIN` 是首次初始化的管理员账号，支持用户名或邮箱格式。初始密码只在数据库没有管理员时使用；已有数据库不会因修改此变量而重置密码。建议使用长随机密码，长度不能超过 72 字节。
- 使用单引号包裹密码，可保留 `$`、`#` 等字符；密码中需要单引号时按 Compose 的 `.env` 转义规则填写。
- `PANEL_VERSION` 是源码构建时显示的版本。构建某个标签时将它设为对应标签；开发分支可保持 `dev`。
- `PANEL_PORT` 仅修改宿主机端口，容器内部固定监听 8080。默认的 `127.0.0.1` 绑定适合宿主机反向代理。

```bash
docker compose up -d --build
docker compose ps
docker compose logs --tail=100 panel
curl -fsS http://127.0.0.1:8080/api/ready
```

配置 HTTPS 反向代理后，通过 `PANEL_BASE_URL` 登录。首次登录成功后，将 `.env` 中的 `PANEL_ADMIN_PASSWORD` 清空，再执行以下命令，从新容器配置中移除初始密码：

```bash
docker compose up -d --no-build --force-recreate
```

镜像用 UID/GID `10001:10001` 运行。Compose 使用只读根文件系统、可写数据卷及临时目录，不需要特权容器、host 网络或 Docker socket。

## HTTPS 与 WebSocket 反向代理

面板内部使用 HTTP，对外必须使用 HTTPS，并透传 WebSocket Upgrade。以下 `location` 配置可放入已有证书的 Nginx HTTPS `server` 中：

```nginx
client_max_body_size 520m;

location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_read_timeout 300s;
    proxy_send_timeout 300s;
    proxy_request_buffering off;
}
```

上传大小和超时设置用于较大的备份恢复。HTTPS 证书需覆盖 `PANEL_BASE_URL` 中的域名。

反向代理本身也在 Docker 中时，可让两个服务加入同一个 Docker 网络，并将上游设为 `panel:8080`。反向代理容器内的 `127.0.0.1` 指向它自己，不能直接使用上面的宿主机上游地址。没有必要为了此用途将面板 8080 端口公开到公网。

## 节点接入

在面板创建服务器，复制节点详情页生成的安装命令，在目标 VPS 的宿主机执行。目标 VPS 仍需 Linux amd64/arm64 和 systemd。面板镜像内同时包含两种架构的 Agent，镜像自身架构不会限制可管理的节点架构。

Agent 不进入容器，sing-box 的安装、启停、更新、配置下发、日志及流量采集仍走现有原生流程。节点不需开放额外管理端口。

## 数据与备份

默认使用 SQLite，`panel-data` 命名卷挂载在 `/app/data`，包含数据库、`.jwt_secret`、数据库迁移备份及恢复文件。必须持久化整个目录，不能只挂载数据库文件。

保持 Compose 项目名和数据卷一致；换目录或使用不同的 `-p` 项目名可能创建一个新卷，看起来像首次安装。可用 `docker compose volumes` 查看当前项目的数据卷。

日常备份使用面板「设置 → 备份」下载 `.tar.gz`，或使用已有 OneDrive 备份。不要直接复制正在写入的 SQLite 文件。

恢复时在「设置 → 恢复」上传备份。面板完成校验和替换后会退出，由 `restart: unless-stopped` 自动启动并打开恢复后的数据库。不要改成 `on-failure`：恢复后的退出码是 0。恢复完成后重新登录并核对节点、用户和订阅。

如需用宿主机目录替代命名卷，将卷配置改为 `./data:/app/data`，事先创建目录并赋予 UID/GID `10001:10001` 读写权限。已有数据请先备份，再处理权限。

面板主机概览仍读取运行环境的 `/proc`，不表示 Docker 的 CPU/内存配额使用率；节点指标继续由节点上的原生 Agent 采集。

## 更新与回退

Docker 模式下，网页展示版本信息和镜像更新提示；原生二进制的在线更新、重新安装、版本回退按钮不显示。镜像中不运行 `install.sh`。

源码构建的更新流程：先下载备份，然后取得目标代码版本，更新 `.env` 的 `PANEL_VERSION`，执行：

```bash
docker compose build --pull panel
docker compose up -d --no-build
docker compose ps
```

数据卷会复用。不要删除数据卷后再更新。

仓库的标签发布工作流会在后端/前端检查及二进制发布成功后构建镜像，通过容器验证后发布到 GHCR，包含 `linux/amd64` 和 `linux/arm64`。仅 `v主版本.次版本.修订号` 格式的正式标签同时更新 `latest`；带预发布后缀的标签不会更新 `latest`。

**只有对应标签的镜像发布任务成功后，才可以使用远端镜像。** 首次发布还需由仓库维护者确认 GHCR 包对部署者可见；公开项目通常将包设为 Public。源码构建不依赖任何已经发布的 GHCR 镜像。

使用已发布镜像时，在 `.env` 将 `PANEL_IMAGE` 设置为 `ghcr.io/hann0w0/singbox-panel:<实际发布的标签>`，然后：

```bash
docker compose pull panel
docker compose up -d --no-build
```

生产建议固定版本标签。镜像版本回退并不等于数据库回退；新版本可能已经迁移数据库。需要回退时使用目标版本及与之匹配的升级前备份，先在独立实例确认恢复正常，再切换流量。

## 从原生面板迁移

1. 在原面板下载完整备份，保存原域名、反向代理配置及当前版本。
2. 用同版本或更新版本构建/启动 Docker 面板，先用临时 HTTPS 域名或独立端口确认可以登录。
3. 切换前停止旧面板服务，避免两套面板同时管理同一批节点：`sudo systemctl stop singbox-panel`。不要停止或卸载节点 Agent、sing-box。
4. 在 Docker 面板恢复原备份，等待自动重启，用原管理员账号登录。
5. 将 Docker 面板的 `PANEL_BASE_URL` 设为原域名，重建容器，将原域名的反向代理切换到容器端口。域名和已恢复的节点凭据保持一致时，Agent 会自动重连。
6. 核对节点连接、用户、订阅及流量记录，确认后禁用旧面板自启：`sudo systemctl disable singbox-panel`。暂时保留旧数据及备份用于回退。

同机迁移时，旧面板和容器必须使用不同的监听端口，直到完成切换。如果公网域名也发生变化，需要更新节点 Agent 的面板地址，不能仅靠数据库恢复自动切换。

## 停止与卸载

```bash
# 停止并移除容器/网络，保留数据卷
docker compose down

# 使用相同项目名与数据卷重新启动
docker compose up -d --no-build
```

`docker compose down -v` 会删除数据库、密钥及卷内备份，只适用于明确要清空该实例全部数据的场景。面板容器的停止或删除不会卸载节点上的 Agent 和 sing-box。

## 开发验证

```bash
docker build -t singbox-panel:docker-test --build-arg VERSION=dev .
python3 scripts/docker-smoke.py --image singbox-panel:docker-test
```

验证脚本需要 Python 3.8+、Docker Compose。它创建随机项目名、临时账号及独立数据卷，检查前端与登录、健康状态、维护能力、两种架构 Agent 下载与校验和、WebSocket 升级、重建持久性和备份恢复后的自动重启，最后删除该测试项目及其卷。它不会在宿主机安装 Agent。CI 在主分支和 PR 中运行该检查。
