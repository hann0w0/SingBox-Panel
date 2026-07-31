#!/bin/sh
# SingBox Panel 一键安装 / 更新脚本
# 用法：curl -fsSL https://raw.githubusercontent.com/hann0w0/SingBox-Panel/main/deploy/install.sh | sudo bash
# 或下载后本地执行：sudo bash install.sh
set -e

###############################################################################
# 颜色与日志工具
###############################################################################
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'

log()  { printf "${GREEN}[INFO]${RESET}  %s\n" "$*"; }
warn() { printf "${YELLOW}[WARN]${RESET}  %s\n" "$*"; }
err()  { printf "${RED}[ERR]${RESET}   %s\n" "$*" >&2; }
die()  { err "$*"; exit 1; }

###############################################################################
# 常量
###############################################################################
REPO="hann0w0/SingBox-Panel"
GHCR_IMAGE="ghcr.io/hann0w0/singbox-panel"
DH_IMAGE="hann0w0/singbox-panel"
PANEL_PORT="${PANEL_PORT:-32334}"
INSTALL_DIR="/opt/singbox-panel"
DATA_DIR="/opt/singbox-panel/data"
AGENTS_DIR="/opt/singbox-panel/dist/agents"
CONF_FILE="/opt/singbox-panel/panel.yaml"
SERVICE_FILE="/etc/systemd/system/singbox-panel.service"
BIN="/usr/local/bin/singbox-panel"
CONTAINER_NAME="singbox-panel"

###############################################################################
# 权限检查
###############################################################################
[ "$(id -u)" = "0" ] || die "请以 root 身份运行（sudo bash install.sh）"

###############################################################################
# 检测已有安装方式（用于更新流程）
###############################################################################
detect_existing() {
  if systemctl is-active --quiet singbox-panel 2>/dev/null && [ -f "$BIN" ]; then
    echo "binary"
  elif docker inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
    echo "docker"
  else
    echo "none"
  fi
}

EXISTING=$(detect_existing)

###############################################################################
# 第一步：询问安装方式
###############################################################################
printf "\n${BOLD}${CYAN}╔══════════════════════════════════════════╗${RESET}\n"
printf "${BOLD}${CYAN}║       SingBox Panel 安装程序             ║${RESET}\n"
printf "${BOLD}${CYAN}╚══════════════════════════════════════════╝${RESET}\n\n"

if [ "$EXISTING" != "none" ]; then
  printf "${YELLOW}检测到已有安装方式：${BOLD}%s${RESET}\n" "$EXISTING"
  printf "将以相同方式执行 ${BOLD}更新${RESET} 操作。\n\n"
  MODE="$EXISTING"
else
  printf "请选择安装方式：\n"
  printf "  ${BOLD}1)${RESET} Docker   — 使用容器运行，依赖 Docker\n"
  printf "  ${BOLD}2)${RESET} Binary   — 原生二进制 + systemd，无需 Docker\n\n"
  printf "输入选项 [1/2，默认 1]：" ; read -r _choice
  case "${_choice:-1}" in
    2|b|binary|Binary) MODE="binary" ;;
    *)                 MODE="docker"  ;;
  esac
fi

printf "\n${GREEN}已选择安装方式：${BOLD}%s${RESET}\n\n" "$MODE"

###############################################################################
# 工具函数：获取架构
###############################################################################
get_arch() {
  case "$(uname -m)" in
    x86_64|amd64)   echo "amd64" ;;
    aarch64|arm64)  echo "arm64" ;;
    *) die "不支持的 CPU 架构：$(uname -m)" ;;
  esac
}

###############################################################################
# 工具函数：获取最新 Release 版本号
###############################################################################
latest_release() {
  curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/'
}

###############################################################################
# ██████████ DOCKER 模式 ██████████
###############################################################################
install_docker() {
  # 依赖检查
  command -v docker >/dev/null 2>&1 || die "未安装 Docker，请先安装：https://docs.docker.com/engine/install/"

  # 选择镜像源（GHCR 或 Docker Hub）
  IMAGE="${SINGBOX_PANEL_IMAGE:-${GHCR_IMAGE}}"
  TAG="${SINGBOX_PANEL_TAG:-latest}"

  log "拉取最新镜像：${IMAGE}:${TAG}"
  docker pull "${IMAGE}:${TAG}"

  # 收集配置（已存在容器时从旧容器环境变量读取默认值）
  if docker inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
    log "检测到已有容器，执行更新..."
    OLD_WEB=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$CONTAINER_NAME" 2>/dev/null | grep '^WEB=' | cut -d= -f2-)
    OLD_ADMIN=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$CONTAINER_NAME" 2>/dev/null | grep '^ADMIN=' | cut -d= -f2-)
    OLD_JWT=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$CONTAINER_NAME" 2>/dev/null | grep '^JWT_SECRET=' | cut -d= -f2-)
    OLD_PORT=$(docker inspect -f '{{range .HostConfig.PortBindings}}{{range .}}{{.HostPort}}{{end}}{{end}}' "$CONTAINER_NAME" 2>/dev/null || echo "$PANEL_PORT")
    WEB="${OLD_WEB:-}"
    ADMIN="${OLD_ADMIN:-admin}"
    JWT_SECRET="${OLD_JWT:-}"
    PANEL_PORT="${OLD_PORT:-$PANEL_PORT}"
  fi

  if [ -z "${WEB:-}" ]; then
    printf "面板域名（含 https://，例如 https://panel.example.com）：" ; read -r WEB
    [ -n "$WEB" ] || die "域名不能为空"
  fi
  if [ -z "${ADMIN_PASSWORD:-}" ]; then
    printf "管理员密码（留空则自动生成）：" ; read -r ADMIN_PASSWORD
  fi
  if [ -z "${JWT_SECRET:-}" ]; then
    JWT_SECRET=$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | xxd -p | tr -d '\n')
  fi

  # 停止旧容器（若有）
  if docker inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
    log "停止并移除旧容器..."
    docker rm -f "$CONTAINER_NAME" >/dev/null
  fi

  # 确保数据卷存在
  docker volume inspect singbox-panel_data >/dev/null 2>&1 || docker volume create singbox-panel_data >/dev/null

  log "启动容器..."
  docker run -d \
    --name "$CONTAINER_NAME" \
    --restart unless-stopped \
    --log-driver local --log-opt max-size=10m --log-opt max-file=3 \
    -p "127.0.0.1:${PANEL_PORT}:32334" \
    -e "ADMIN=${ADMIN:-admin}" \
    -e "ADMIN_PASSWORD=${ADMIN_PASSWORD}" \
    -e "WEB=${WEB}" \
    -e "JWT_SECRET=${JWT_SECRET}" \
    -v singbox-panel_data:/data \
    "${IMAGE}:${TAG}"

  log "等待面板就绪..."
  i=0
  while [ "$i" -lt 20 ]; do
    if docker exec "$CONTAINER_NAME" wget -qO- "http://localhost:32334/api/health" 2>/dev/null | grep -q '"ok"'; then
      break
    fi
    i=$((i+1)); sleep 1
  done

  print_docker_done
}

print_docker_done() {
  printf "\n${GREEN}${BOLD}✔ Docker 安装/更新完成！${RESET}\n"
  printf "  面板监听：${BOLD}127.0.0.1:${PANEL_PORT}${RESET}\n"
  printf "  请配置反向代理（Caddy/Nginx）将 %s 转发到上述端口。\n\n" "${WEB:-<your-domain>"
  printf "  ${CYAN}常用命令：${RESET}\n"
  printf "    查看日志：docker logs -f %s\n" "$CONTAINER_NAME"
  printf "    重启面板：docker restart %s\n" "$CONTAINER_NAME"
  printf "    更新面板：重新执行本安装脚本\n\n"
}

###############################################################################
# ██████████ BINARY 模式 ██████████
###############################################################################
install_binary() {
  ARCH=$(get_arch)

  # 获取版本
  if [ -z "${PANEL_VERSION:-}" ]; then
    log "获取最新版本号..."
    PANEL_VERSION=$(latest_release)
    [ -n "$PANEL_VERSION" ] || die "无法获取 Release 版本号，请检查网络或手动指定 PANEL_VERSION=vX.Y.Z"
  fi
  log "版本：${PANEL_VERSION}，架构：${ARCH}"

  # 创建目录
  mkdir -p "$INSTALL_DIR" "$DATA_DIR" "$AGENTS_DIR"

  # 下载面板二进制
  PANEL_URL="https://github.com/${REPO}/releases/download/${PANEL_VERSION}/singbox-panel-linux-${ARCH}"
  log "下载面板二进制..."
  TMP_BIN="$(mktemp /tmp/.singbox-panel.XXXXXX)"
  trap 'rm -f "$TMP_BIN"' EXIT
  curl -fsSL "$PANEL_URL" -o "$TMP_BIN" || die "下载失败：$PANEL_URL"
  chmod +x "$TMP_BIN"

  # 下载 agents 包（tar.gz，解压到 dist/agents/）
  AGENTS_URL="https://github.com/${REPO}/releases/download/${PANEL_VERSION}/singbox-panel-agents-${PANEL_VERSION}.tar.gz"
  log "下载 Agent 二进制包..."
  TMP_AGENTS="$(mktemp /tmp/.singbox-panel-agents.XXXXXX.tar.gz)"
  trap 'rm -f "$TMP_BIN" "$TMP_AGENTS"' EXIT
  curl -fsSL "$AGENTS_URL" -o "$TMP_AGENTS" || die "下载 Agent 包失败：$AGENTS_URL"
  tar -xzf "$TMP_AGENTS" -C "$AGENTS_DIR" --strip-components=0
  log "Agent 二进制已解压到 ${AGENTS_DIR}"

  # 下载前端静态文件包
  WEB_DIR="${INSTALL_DIR}/web/dist"
  WEB_URL="https://github.com/${REPO}/releases/download/${PANEL_VERSION}/singbox-panel-web-${PANEL_VERSION}.tar.gz"
  log "下载前端静态文件包..."
  TMP_WEB="$(mktemp /tmp/.singbox-panel-web.XXXXXX.tar.gz)"
  trap 'rm -f "$TMP_BIN" "$TMP_AGENTS" "$TMP_WEB"' EXIT
  if curl -fsSL "$WEB_URL" -o "$TMP_WEB" 2>/dev/null; then
    mkdir -p "$WEB_DIR"
    tar -xzf "$TMP_WEB" -C "$WEB_DIR"
    log "前端静态文件已解压到 ${WEB_DIR}"
  else
    warn "前端静态文件包下载失败，跳过（可手动放置到 ${WEB_DIR}）"
  fi

  # 停旧服务（更新时）
  if systemctl is-active --quiet singbox-panel 2>/dev/null; then
    log "停止旧服务..."
    systemctl stop singbox-panel
  fi

  # 替换二进制
  mv -f "$TMP_BIN" "$BIN"
  chmod +x "$BIN"
  log "面板二进制已安装到 ${BIN}"

  # 生成/更新配置文件（已存在时跳过，保留用户设置）
  if [ ! -f "$CONF_FILE" ]; then
    # 交互采集配置
    printf "面板域名（含 https://，例如 https://panel.example.com）：" ; read -r WEB
    [ -n "$WEB" ] || die "域名不能为空"
    printf "管理员邮箱 [默认 admin@example.com]：" ; read -r ADMIN_EMAIL
    ADMIN_EMAIL="${ADMIN_EMAIL:-admin@example.com}"
    printf "管理员密码（留空则自动生成）：" ; read -r ADMIN_PASSWORD
    if [ -z "$ADMIN_PASSWORD" ]; then
      ADMIN_PASSWORD=$(openssl rand -hex 8 2>/dev/null || head -c 8 /dev/urandom | xxd -p | tr -d '\n')
      printf "${YELLOW}自动生成管理员密码：${BOLD}%s${RESET}  请保存！\n" "$ADMIN_PASSWORD"
    fi
    JWT_SECRET=$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | xxd -p | tr -d '\n')

    cat > "$CONF_FILE" <<EOF
# SingBox Panel 配置文件 — 由安装脚本生成，可手动修改后重启生效

listen: ":${PANEL_PORT}"
base_url: "${WEB}"
jwt_secret: "${JWT_SECRET}"

agents_dir: "${AGENTS_DIR}"
web_dir: "${INSTALL_DIR}/web/dist"

database:
  driver: sqlite
  dsn: "${DATA_DIR}/singbox-panel.db"

admin:
  email: "${ADMIN_EMAIL}"
  password: "${ADMIN_PASSWORD}"
EOF
    chmod 600 "$CONF_FILE"
    log "配置文件已写入 ${CONF_FILE}"
  else
    warn "配置文件已存在，跳过重新生成（${CONF_FILE}）"
    warn "如需修改，请手动编辑后执行 systemctl restart singbox-panel"
  fi

  # 注册 systemd 服务
  cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=SingBox Panel
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
ExecStart=${BIN} --config ${CONF_FILE}
Restart=always
RestartSec=5s
LimitNOFILE=infinity
WorkingDirectory=${INSTALL_DIR}

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable singbox-panel
  systemctl restart singbox-panel

  # 等待就绪
  log "等待面板就绪..."
  i=0
  while [ "$i" -lt 20 ]; do
    if curl -fsSL "http://127.0.0.1:${PANEL_PORT}/api/health" 2>/dev/null | grep -q '"ok"'; then
      break
    fi
    i=$((i+1)); sleep 1
  done

  print_binary_done
}

print_binary_done() {
  printf "\n${GREEN}${BOLD}✔ Binary 安装/更新完成！${RESET}\n"
  printf "  安装目录：${BOLD}%s${RESET}\n" "$INSTALL_DIR"
  printf "  配置文件：${BOLD}%s${RESET}\n" "$CONF_FILE"
  printf "  面板监听：${BOLD}127.0.0.1:%s${RESET}\n" "$PANEL_PORT"
  printf "\n  ${CYAN}常用命令：${RESET}\n"
  printf "    查看状态：systemctl status singbox-panel\n"
  printf "    查看日志：journalctl -u singbox-panel -f\n"
  printf "    重启面板：systemctl restart singbox-panel\n"
  printf "    更新面板：重新执行本安装脚本\n\n"
}

###############################################################################
# 入口：按 MODE 路由
###############################################################################
case "$MODE" in
  docker) install_docker ;;
  binary) install_binary ;;
  *) die "未知安装方式：$MODE" ;;
esac
