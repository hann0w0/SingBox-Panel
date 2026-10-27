# Docker 部署

项目默认使用[原生面板安装](../README.md#原生面板安装默认)。本页提供可选的 `docker run` 和 Docker Compose 两种方式，任选其一。

Docker 只运行管理面板。每个被管理的 VPS 继续使用面板生成的安装命令部署原生 Agent，Agent 通过 WSS 主动连接面板，并由宿主机的 systemd 或 Alpine OpenRC 管理该 VPS 上的 sing-box。即使节点与面板在同一台机器，也在宿主机上安装 Agent。

## 部署前准备

需要 Docker Engine、可访问的 HTTPS 域名及支持 WebSocket 的反向代理；Compose 方式另需 Docker Compose 插件。镜像支持 Linux amd64、arm64，无需 Git、Go 或 Node.js。

默认使用 `ghcr.io/hann0w0/singbox-panel:latest`。`latest` 是镜像别名，检查通过的 `main` 构建和正式版本发布都会更新它，无需先创建 `v1.0.0` 这样的 Git 标签。镜像发布成功且包已公开后才能拉取；fork 用户将镜像地址改为自己的 GHCR 命名空间，例如 `ghcr.io/tokinx/singbox-panel:latest`。

默认将宿主机的 `127.0.0.1:32334` 映射到容器内 `8080`，与原生安装的默认对外端口一致。容器内部监听和健康检查仍使用 `8080`。

## 方式一：docker run

替换下面的 HTTPS 域名、管理员账号和初始密码后执行：

```bash
docker pull ghcr.io/hann0w0/singbox-panel:latest
docker run -d \
  --name singbox-panel \
  --restart unless-stopped \
  --stop-timeout 30 \
  --publish 127.0.0.1:32334:8080 \
  --mount type=volume,source=singbox-panel-data,target=/app/data \
  --env SINGBOX_PANEL_BASE_URL='https://panel.example.com' \
  --env SINGBOX_PANEL_ADMIN_EMAIL='admin' \
  --env SINGBOX_PANEL_ADMIN_PASSWORD='请替换为自己的初始密码' \
  --read-only \
  --tmpfs /tmp:rw,noexec,nosuid,size=64m,mode=1777 \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --log-driver json-file \
  --log-opt max-size=10m \
  --log-opt max-file=3 \
  ghcr.io/hann0w0/singbox-panel:latest
```

查看状态和日志：

```bash
docker ps --filter name=singbox-panel
docker logs --tail=100 singbox-panel
curl -fsS http://127.0.0.1:32334/api/ready
```

参数说明：

| 参数 | 作用 |
| --- | --- |
| `--name singbox-panel` | 固定容器名，便于查看日志、更新和停止 |
| `--restart unless-stopped` | 容器退出后自动重启，手动停止后保持停止 |
| `--publish 127.0.0.1:32334:8080` | 仅向宿主机反向代理开放 HTTP 端口 |
| `--mount …target=/app/data` | 将数据库、密钥及恢复文件保存在命名卷中 |
| `SINGBOX_PANEL_BASE_URL` | 面板对外 HTTPS 地址，不带路径；用于 Agent 和订阅 |
| `SINGBOX_PANEL_ADMIN_EMAIL` | 首次创建的管理员账号，支持用户名或邮箱格式 |
| `SINGBOX_PANEL_ADMIN_PASSWORD` | 首次初始化密码，最多 72 字节 |
| `--read-only`、`--tmpfs` | 根文件系统只读，临时目录可写 |

镜像已使用 UID/GID `10001:10001` 运行，无需特权模式、host 网络或 Docker socket。

## 方式二：Docker Compose

在新目录创建 `compose.yaml`，粘贴下方配置并替换域名、管理员账号和初始密码。本示例直接在 YAML 中配置，不需要 `.env`；密码中的字面 `$` 需写成 `$$`，避免 Compose 将其当作变量替换。

```yaml
services:
  panel:
    image: ghcr.io/hann0w0/singbox-panel:latest
    restart: unless-stopped
    stop_grace_period: 30s
    ports:
      - "127.0.0.1:32334:8080"
    environment:
      SINGBOX_PANEL_BASE_URL: "https://panel.example.com"
      SINGBOX_PANEL_ADMIN_EMAIL: "admin"
      SINGBOX_PANEL_ADMIN_PASSWORD: "请替换为自己的初始密码"
    volumes:
      - panel-data:/app/data
    read_only: true
    tmpfs:
      - /tmp:rw,noexec,nosuid,size=64m,mode=1777
    cap_drop:
      - ALL
    security_opt:
      - no-new-privileges:true
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"

volumes:
  panel-data:
```

启动并查看状态：

```bash
chmod 600 compose.yaml
docker compose pull panel
docker compose up -d
docker compose ps
docker compose logs --tail=100 panel
curl -fsS http://127.0.0.1:32334/api/ready
```

仓库及 Release 提供的 Compose 模板使用 `.env` 传入相同参数，也可以继续使用；本页内联示例是单文件部署。现有 `.env` 如果仍设置 `PANEL_PORT=8080`，需要改为 `PANEL_PORT=32334` 后执行 `docker compose up -d` 重建端口映射，并同步调整宿主机反向代理。

两种方式的管理员账号和密码都只在数据库没有管理员时使用，修改环境变量不会重置已有账号。首次登录成功后，可移除 `docker run` 中的初始密码参数再重建容器；Compose 则将 YAML 中的 `SINGBOX_PANEL_ADMIN_PASSWORD` 改为空字符串后执行 `docker compose up -d`。保留原数据卷即可继续使用已有账号。

## HTTPS 与 WebSocket 反向代理

面板内部使用 HTTP，对外必须使用 HTTPS，并透传 WebSocket Upgrade。以下 `location` 配置可放入已有证书的 Nginx HTTPS `server` 中：

```nginx
client_max_body_size 520m;

location / {
    proxy_pass http://127.0.0.1:32334;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    # 必须用真实来源地址覆盖 X-Real-IP，否则客户端可自行伪造该头绕过登录限流
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_read_timeout 300s;
    proxy_send_timeout 300s;
    proxy_request_buffering off;
}
```

上传大小和超时设置用于较大的备份恢复。HTTPS 证书需覆盖 `SINGBOX_PANEL_BASE_URL` 中的域名。

面板只从本机或内网的反向代理读取 `X-Real-IP`，用于登录限流和登录记录。反向代理**必须覆盖**这个头：Nginx 和 Caddy 默认都会把客户端自带的 `X-Real-IP` 原样转发，不覆盖时攻击者每次请求都能换一个"来源 IP"，让按 IP 的登录限流失效。Caddy 写法：

```caddy
panel.example.com {
    reverse_proxy 127.0.0.1:32334 {
        header_up X-Real-IP {remote_host}
    }
}
```

如果同一 Docker 网络或内网里还有其他不可信的容器/主机能直连面板，用 `SINGBOX_PANEL_TRUSTED_PROXIES`（逗号分隔的 IP 或 CIDR，原生安装对应 `panel.yaml` 的 `trusted_proxies` 列表）只信任反向代理本身，例如 `SINGBOX_PANEL_TRUSTED_PROXIES=172.18.0.2`。未设置时信任所有回环、私有和链路本地地址。

反向代理本身也在 Docker 中时，可让两个服务加入同一个 Docker 网络，并将上游设为 `panel:8080`。反向代理容器内的 `127.0.0.1` 指向它自己，不能直接使用上面的宿主机上游地址。容器间仍使用内部端口 `8080`，无需为此将面板端口公开到公网。

## 节点接入

在面板创建服务器，复制节点详情页生成的安装命令，在目标 VPS 的宿主机执行。目标 VPS 需为 Linux amd64/arm64，并运行 systemd 或 Alpine OpenRC。Alpine 最小系统可直接使用生成的命令：脚本会在 root 下执行，或通过 `sudo`/`doas` 提权，并自动补齐 `curl` 依赖；安装 sing-box 时保留默认的官方脚本方式。面板镜像内同时包含两种架构的 Agent，镜像自身架构不会限制可管理的节点架构。

Agent 不进入容器，sing-box 的安装、启停、更新、配置下发、日志及流量采集仍走现有原生流程。systemd 节点从 journal 读取日志；Alpine/OpenRC 节点使用 `/var/log/sing-box.log`，Agent 本身写入 `/var/log/singbox-panel-agent.log`，两者应纳入节点现有的日志轮转策略。节点不需开放额外管理端口。

## 数据与备份

默认使用 SQLite，整个 `/app/data` 目录必须持久化，包含数据库、`.jwt_secret`、数据库迁移备份及恢复文件，不能只挂载数据库文件。`docker run` 示例使用 `singbox-panel-data` 卷；Compose 示例使用项目管理的 `panel-data` 卷，实际名称通常为 `<项目名>_panel-data`。

这两种方式默认使用不同的数据卷。切换方式时先备份，再在新实例恢复，或明确复用已有数据卷；不要让两个面板同时写入同一 SQLite 数据库。

保持 Compose 项目名和数据卷一致；换目录或使用不同的 `-p` 项目名可能创建一个新卷，看起来像首次安装。可用 `docker compose volumes` 查看当前项目的数据卷。

日常备份使用面板「设置 → 备份」下载 `.tar.gz`，或使用已有 OneDrive 备份。不要直接复制正在写入的 SQLite 文件。

备份包含 `jwt_secret` 和全部节点密钥，拿到明文备份就能伪造管理员登录。因此 OneDrive 同步必须先在「设置 → 备份」设置备份口令（至少 12 个字符）：上传的是用该口令加密的 `singbox-panel-backup.tar.gz.enc`（Argon2id + AES-256-GCM），未设置口令时不会上传。旧版本留下的明文 `singbox-panel-backup.tar.gz` 会在第一次加密同步成功后自动删除。注意 OneDrive 删除的文件（连同它的历史版本）会先进入回收站，保留最多 30 天（工作或学校账号可能更久）；升级后请到 OneDrive 网页版清空回收站里的这个文件，明文副本才算真正清除。口令只以加密形式保存在面板数据库中，请另行妥善保管：在新主机恢复，或手动上传 `.enc` 文件恢复时都需要输入它，丢失后无法解密。

恢复时在「设置 → 恢复」上传备份。面板完成校验和替换后会退出，由 `restart: unless-stopped` 自动启动并打开恢复后的数据库。不要改成 `on-failure`：恢复后的退出码是 0。恢复完成后重新登录并核对节点、用户和订阅。

如需用宿主机目录替代命名卷，将卷配置改为 `./data:/app/data`，事先创建目录并赋予 UID/GID `10001:10001` 读写权限。已有数据请先备份，再处理权限。

面板主机概览仍读取运行环境的 `/proc`，不表示 Docker 的 CPU/内存配额使用率；节点指标继续由节点上的原生 Agent 采集。

## 更新与回退

Docker 模式下，网页展示版本信息和镜像更新提示；原生二进制的在线更新、重新安装、版本回退按钮不显示。镜像中不运行 `install.sh`。

默认使用 `latest`，无需手动修改版本号。更新前先在面板下载备份。

### docker run 更新

```bash
docker pull ghcr.io/hann0w0/singbox-panel:latest
docker stop singbox-panel
docker rm singbox-panel
```

然后重新执行上方完整的 `docker run` 命令，保留原域名、端口及 `singbox-panel-data` 卷名。已有管理员时可省略初始密码参数；不要删除数据卷。

### Compose 更新

在原 `compose.yaml` 目录执行：

```bash
docker compose pull panel
docker compose up -d
docker compose ps
```

数据卷会复用。不要删除数据卷或覆盖已有配置文件。

## 自动构建与镜像标签

仓库的 `Publish Docker image` 工作流使用 GitHub 自带的 `GITHUB_TOKEN` 和 `packages: write` 发布到 `ghcr.io/<仓库所有者>/<小写仓库名>`，无需额外配置 PAT。镜像包含 `linux/amd64`、`linux/arm64`。

| 触发方式 | 前置检查 | 发布标签 |
| --- | --- | --- |
| 推送到 `main` | `Check changes` 的后端/前端检查和容器验证全部通过 | `latest`、`edge`、`sha-<12位提交哈希>` |
| 推送正式标签，如 `v1.2.3` | 版本发布检查、二进制打包和容器验证通过 | `v1.2.3`、`latest` |
| 推送预发布标签，如 `v1.2.3-rc.1` | 同上 | 仅该预发布版本，不更新 `latest` |
| Actions 手动运行 `Publish Docker image` | 重新执行后端、前端及容器检查 | 按所选 `main` 或版本标签决定，其他分支拒绝发布 |

正式发布的 Git 版本标签必须与 `cmd/agent/VERSION` 一致。镜像标签和应用版本号是两回事：`latest` 指向已通过检查的构建，镜像内部仍记录源码版本及提交后缀，而不是把程序版本改成 `latest`。`edge` 保留为主分支构建的兼容别名；预发布 Git 标签不更新 `latest`。PR 只运行检查，不登录 GHCR，也不发布镜像。

手动发布 `latest` 时，在 Actions 中运行 **Publish Docker image**，选择 `main` 分支即可；不要创建名为 `latest` 的 Git 标签。发布后 CI 逐一检查目标镜像标签可查询，再生成部署附件。

每次成功发布后，Actions 页面提供 `docker-deploy-<标签>` 附件，其中的 `compose.yaml` 和 `panel.env.example` 已指向实际发布仓库。下载解压，把 `panel.env.example` 重命名为 `.env`，填写域名和账号即可运行，无需源码。正式 Release 也包含这两个部署文件，并纳入 `checksums.txt`。

**首次发布后，需要维护者在 GHCR 包的 Package settings 中将可见性设为 Public，用户才能匿名拉取。** 新包默认是 Private；仓库公开不代表包自动公开。私有部署也可选择保留私有包，并由部署者先执行 `docker login ghcr.io`。

首次成功的 main 构建即可生成 `latest` 和 `docker-deploy-latest` 附件，不依赖创建正式 Release。`latest` 会跟随新的已验证构建变化；需要固定发布版本时，使用 `vX.Y.Z` 标签。旧版 CI 只生成 `edge`，需要在包含此修复的 `main` 上重新运行发布流程，才会补出 `latest`；直接重跑旧提交仍使用旧逻辑。

如需固定版本，可将 `docker run` 命令或 Compose 的 `image` 中的 `latest` 改成已发布的版本标签；使用 `.env` 模板时修改 `PANEL_IMAGE`。镜像版本回退并不等于数据库回退；新版本可能已经迁移数据库。需要回退时使用目标版本及与之匹配的升级前备份，先在独立实例确认恢复正常，再切换流量。

## 从原生面板迁移

1. 在原面板下载完整备份，保存原域名、反向代理配置及当前版本。
2. 用同版本或更新版本构建/启动 Docker 面板，先用临时 HTTPS 域名或独立端口确认可以登录。
3. 切换前停止旧面板服务，避免两套面板同时管理同一批节点：`sudo systemctl stop singbox-panel`。不要停止或卸载节点 Agent、sing-box。
4. 在 Docker 面板恢复原备份，等待自动重启，用原管理员账号登录。
5. 将 Docker 面板的 `SINGBOX_PANEL_BASE_URL`（使用 `.env` 模板时为 `PANEL_BASE_URL`）设为原域名，重建容器，将原域名的反向代理切换到容器端口。域名和已恢复的节点凭据保持一致时，Agent 会自动重连。
6. 核对节点连接、用户、订阅及流量记录，确认后禁用旧面板自启：`sudo systemctl disable singbox-panel`。暂时保留旧数据及备份用于回退。

同机迁移时，旧面板和容器必须使用不同的监听端口，直到完成切换。如果公网域名也发生变化，需要更新节点 Agent 的面板地址，不能仅靠数据库恢复自动切换。

## 停止与卸载

`docker run` 部署：

```bash
# 仅停止，之后可用 docker start singbox-panel 启动
docker stop singbox-panel
# 删除容器，保留 singbox-panel-data 数据卷
docker rm singbox-panel
```

Compose 部署在原配置目录执行：

```bash
# 停止并移除容器/网络，保留数据卷
docker compose down
# 使用相同项目名与数据卷重新启动
docker compose up -d
```

`docker compose down -v` 或删除对应数据卷会清空数据库、密钥及卷内备份。面板容器的停止或删除不会卸载节点上的 Agent 和 sing-box。

## 开发者：可选源码构建与验证

需要开发或定制时才 clone 仓库。生产 `compose.yaml` 没有构建步骤；源码构建通过单独的覆盖文件启用：

```bash
git clone https://github.com/hann0w0/SingBox-Panel.git
cd SingBox-Panel
cp .env.example .env
# 编辑域名、管理员账号和初始密码，并改为：
# PANEL_IMAGE=singbox-panel:local
# PANEL_VERSION=dev
docker compose -f compose.yaml -f compose.build.yaml up -d --build
```

后续源码更新使用同样的两个 `-f` 参数重新构建；`PANEL_VERSION` 仅用于源码构建时的版本显示。不要让本地定制镜像占用官方 GHCR 镜像标签。

```bash
python3 scripts/test-docker-metadata.py
docker build -t singbox-panel:docker-test --build-arg VERSION=dev .
python3 scripts/docker-smoke.py --image singbox-panel:docker-test
```

验证脚本需要 Python 3.8+、Docker Compose。元数据测试覆盖 main、正式版、预发布、fork 和拒绝非法引用。容器测试只把 Compose 和环境文件复制到临时部署目录，不依赖 Dockerfile 或源码；它创建独立数据卷，检查前端与登录、健康状态、两种架构 Agent 下载与校验和、WebSocket 升级、重建持久性和恢复后的自动重启，最后删除测试项目及其卷。它不会在宿主机安装 Agent。CI 在主分支和 PR 中运行此检查，发布前再验证候选镜像。
