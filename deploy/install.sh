#!/bin/sh
# SingBox Panel 一键安装 / 更新脚本
# 用法：curl -fsSL https://raw.githubusercontent.com/hann0w0/SingBox-Panel/main/deploy/install.sh | sudo sh
# 或下载后本地执行：sudo sh install.sh
#
# 非交互安装（CI / 自动化）：
#   WEB=https://panel.example.com ADMIN_PASSWORD=xxx MODE=binary sh install.sh
set -eu

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

# 允许通过环境变量预置，避免非交互环境下无法提问。
MODE="${MODE:-}"
WEB="${WEB:-}"
ADMIN_EMAIL="${ADMIN_EMAIL:-}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-}"
JWT_SECRET="${JWT_SECRET:-}"
PANEL_VERSION="${PANEL_VERSION:-}"

TMP_FILES=""
cleanup() { [ -z "$TMP_FILES" ] || rm -f $TMP_FILES; }
trap cleanup EXIT

# mktemp 模板必须以连续的 X 结尾，后缀会被原样保留而不替换，所以这里不加 .tar.gz。
new_tmp() {
  _t="$(mktemp /tmp/.singbox-panel.XXXXXX)" || die "无法创建临时文件"
  TMP_FILES="$TMP_FILES $_t"
  printf '%s' "$_t"
}

###############################################################################
# 权限检查
###############################################################################
[ "$(id -u)" = "0" ] || die "请以 root 身份运行（sudo sh install.sh）"

###############################################################################
# 交互输入：curl | sh 时 stdin 被脚本自身占用，需另开 /dev/tty
###############################################################################
PROMPT_FD=""
if [ -t 0 ]; then
  PROMPT_FD=0
elif { exec 3<>/dev/tty; } 2>/dev/null; then
  PROMPT_FD=3
fi

REPLY_VALUE=""
prompt_read() {
  REPLY_VALUE=""
  case "$PROMPT_FD" in
    0) printf "%s" "$1"; IFS= read -r REPLY_VALUE ;;
    3) printf "%s" "$1" >&3; IFS= read -r REPLY_VALUE <&3 ;;
    *) die "当前为非交互环境，无法读取输入。请用环境变量提供配置，例如：
  WEB=https://panel.example.com ADMIN_PASSWORD=xxx MODE=binary sh install.sh" ;;
  esac
}

###############################################################################
# 工具函数
###############################################################################
rand_hex() {
  openssl rand -hex "$1" 2>/dev/null && return 0
  od -An -N "$1" -tx1 /dev/urandom | tr -d ' \n'
}

get_arch() {
  case "$(uname -m)" in
    x86_64|amd64)   echo "amd64" ;;
    aarch64|arm64)  echo "arm64" ;;
    *) die "不支持的 CPU 架构：$(uname -m)" ;;
  esac
}

latest_release() {
  _body="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null)" || {
    die "无法访问 GitHub API（可能是网络问题或匿名调用限流 60 次/小时）。
可手动指定版本：PANEL_VERSION=vX.Y.Z sh install.sh"
  }
  printf '%s' "$_body" \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
    | head -n1
}

# 面板地址归一化：仅接受 HTTPS 域名，拒绝 http / 裸 IP / localhost / 带端口。
# base_url 会被写进订阅链接与 Agent 一键安装命令，填错会导致全量节点不可用。
normalize_domain() {
  _d="$1"
  case "$_d" in
    https://*) _d="${_d#https://}" ;;
    http://*)  die "面板地址必须使用 HTTPS：$1" ;;
    *://*)     die "面板地址必须使用 HTTPS：$1" ;;
  esac
  _d="${_d%/}"
  [ -n "$_d" ] || die "域名不能为空"
  case "$_d" in
    *[!-A-Za-z0-9.]*) die "域名只能包含字母、数字、-、.，请仅填域名（例如 panel.example.com）：$1" ;;
  esac
  case "$_d" in
    *.*) : ;;
    *)   die "请填写完整域名，例如 panel.example.com（当前：$1）" ;;
  esac
  case "$_d" in
    localhost|localhost.*) die "不能使用 localhost 作为面板域名" ;;
  esac
  if printf '%s' "$_d" | grep -Eq '^([0-9]{1,3}\.){3}[0-9]{1,3}$'; then
    die "不支持使用公网 IP 直连，请填写域名：$1"
  fi
  printf '%s' "https://${_d}"
}

ask_domain() {
  while [ -z "$WEB" ]; do
    prompt_read "面板域名（含 https://，例如 https://panel.example.com）："
    [ -n "$REPLY_VALUE" ] || { warn "域名不能为空"; continue; }
    WEB="$REPLY_VALUE"
  done
  WEB="$(normalize_domain "$WEB")"
}

ask_password() {
  if [ -z "$ADMIN_PASSWORD" ]; then
    prompt_read "管理员密码（留空则自动生成）："
    ADMIN_PASSWORD="$REPLY_VALUE"
  fi
  if [ -z "$ADMIN_PASSWORD" ]; then
    ADMIN_PASSWORD="$(rand_hex 12)"
    printf "${YELLOW}自动生成管理员密码：${BOLD}%s${RESET}  请立即保存！\n" "$ADMIN_PASSWORD"
  fi
  # bcrypt 只取前 72 字节，超长密码会被静默截断。
  _len="$(LC_ALL=C printf '%s' "$ADMIN_PASSWORD" | wc -c | tr -d ' ')"
  [ "$_len" -le 72 ] || die "管理员密码不能超过 72 字节（bcrypt 限制）"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

CHECKSUMS_FILE=""
verify_checksum() {
  _file="$1"; _name="$2"
  [ -n "$CHECKSUMS_FILE" ] && [ -f "$CHECKSUMS_FILE" ] || return 0
  _want="$(awk -v n="$_name" '$2 == n || $2 == "*" n {print $1; exit}' "$CHECKSUMS_FILE")"
  [ -n "$_want" ] || { warn "checksums.txt 未包含 ${_name}，跳过校验"; return 0; }
  _got="$(sha256_of "$_file")"
  [ -n "$_got" ] || { warn "系统缺少 sha256sum/shasum，跳过校验"; return 0; }
  [ "$_want" = "$_got" ] || die "SHA256 校验失败：${_name}
  期望：${_want}
  实际：${_got}"
  log "校验通过：${_name}"
}

# 从已有配置读取实际监听端口，避免更新时用默认端口去探活。
conf_listen_port() {
  [ -f "$CONF_FILE" ] || return 1
  sed -n 's/^listen:[[:space:]]*"\{0,1\}\([^"#]*\).*/\1/p' "$CONF_FILE" \
    | head -n1 | sed 's/.*://' | tr -d '[:space:]'
}

###############################################################################
# 检测已有安装方式（用于更新流程）
# 注意：不能用 "服务是否 active" 判断——服务 stopped/failed 时会被误判成未安装，
# 导致在同一台机器上交叉安装出两套。
###############################################################################
detect_existing() {
  if [ -f "$BIN" ] && [ -f "$SERVICE_FILE" ]; then
    echo "binary"
  elif command -v docker >/dev/null 2>&1 && docker inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
    echo "docker"
  else
    echo "none"
  fi
}

EXISTING="$(detect_existing)"

###############################################################################
# 第一步：确定安装方式
###############################################################################
printf "\n${BOLD}${CYAN}╔══════════════════════════════════════════╗${RESET}\n"
printf "${BOLD}${CYAN}║       SingBox Panel 安装程序             ║${RESET}\n"
printf "${BOLD}${CYAN}╚══════════════════════════════════════════╝${RESET}\n\n"

if [ -n "$MODE" ]; then
  case "$MODE" in
    docker|binary) : ;;
    *) die "未知安装方式：$MODE（可选 docker / binary）" ;;
  esac
  printf "使用指定安装方式：${BOLD}%s${RESET}\n\n" "$MODE"
elif [ "$EXISTING" != "none" ]; then
  printf "${YELLOW}检测到已有安装方式：${BOLD}%s${RESET}\n" "$EXISTING"
  printf "将以相同方式执行 ${BOLD}更新${RESET} 操作。\n\n"
  MODE="$EXISTING"
else
  printf "请选择安装方式：\n"
  printf "  ${BOLD}1)${RESET} Docker   — 使用容器运行，依赖 Docker\n"
  printf "  ${BOLD}2)${RESET} Binary   — 原生二进制 + systemd，无需 Docker\n\n"
  prompt_read "输入选项 [1/2，默认 1]："
  case "${REPLY_VALUE:-1}" in
    2|b|binary|Binary) MODE="binary" ;;
    *)                 MODE="docker"  ;;
  esac
fi

printf "\n${GREEN}已选择安装方式：${BOLD}%s${RESET}\n\n" "$MODE"

###############################################################################
# ██████████ DOCKER 模式 ██████████
###############################################################################
install_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    log "未检测到 Docker，使用官方脚本安装..."
    _dtmp="$(new_tmp)"
    curl -fsSL https://get.docker.com -o "$_dtmp" || die "下载 Docker 安装脚本失败"
    sh "$_dtmp" || die "Docker 安装失败，请手动安装：https://docs.docker.com/engine/install/"
  fi
  command -v systemctl >/dev/null 2>&1 && systemctl enable --now docker >/dev/null 2>&1 || true
  docker info >/dev/null 2>&1 || die "Docker 守护进程不可用"

  IMAGE="${SINGBOX_PANEL_IMAGE:-${DH_IMAGE}}"
  TAG="${SINGBOX_PANEL_TAG:-latest}"

  log "拉取镜像：${IMAGE}:${TAG}"
  if ! docker pull "${IMAGE}:${TAG}"; then
    if [ "$IMAGE" = "$DH_IMAGE" ]; then
      IMAGE="$GHCR_IMAGE"
      log "Docker Hub 拉取失败，改用 ${IMAGE}:${TAG}"
      docker pull "${IMAGE}:${TAG}" || die "Docker Hub 与 GHCR 均拉取失败"
    else
      die "拉取镜像失败：${IMAGE}:${TAG}"
    fi
  fi

  # 已有容器：继承旧配置作为默认值
  HAD_PREVIOUS=0
  if docker inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
    HAD_PREVIOUS=1
    log "检测到已有容器，执行更新..."
    _env="$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$CONTAINER_NAME" 2>/dev/null || true)"
    [ -n "$WEB" ]            || WEB="$(printf '%s' "$_env" | sed -n 's/^WEB=//p' | head -n1)"
    [ -n "$ADMIN_EMAIL" ]    || ADMIN_EMAIL="$(printf '%s' "$_env" | sed -n 's/^ADMIN=//p' | head -n1)"
    [ -n "$JWT_SECRET" ]     || JWT_SECRET="$(printf '%s' "$_env" | sed -n 's/^JWT_SECRET=//p' | head -n1)"
    _oldport="$(docker inspect -f '{{range .HostConfig.PortBindings}}{{range .}}{{.HostPort}}{{end}}{{end}}' "$CONTAINER_NAME" 2>/dev/null || true)"
    [ -z "$_oldport" ] || PANEL_PORT="$_oldport"
  fi

  ask_domain
  ADMIN_EMAIL="${ADMIN_EMAIL:-admin}"
  ask_password

  docker volume inspect singbox-panel_data >/dev/null 2>&1 || docker volume create singbox-panel_data >/dev/null

  # 先改名保留旧容器，新容器探活成功后再删除，失败可回滚。
  if [ "$HAD_PREVIOUS" = "1" ]; then
    docker rm -f "${CONTAINER_NAME}-rollback" >/dev/null 2>&1 || true
    docker stop "$CONTAINER_NAME" >/dev/null 2>&1 || true
    docker rename "$CONTAINER_NAME" "${CONTAINER_NAME}-rollback"
  fi

  rollback_docker() {
    docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
    if [ "$HAD_PREVIOUS" = "1" ]; then
      docker rename "${CONTAINER_NAME}-rollback" "$CONTAINER_NAME" >/dev/null 2>&1 || true
      docker start "$CONTAINER_NAME" >/dev/null 2>&1 || true
      err "已回滚到更新前的容器"
    fi
  }

  log "启动容器..."
  # JWT_SECRET 不主动生成：留空时面板会把随机密钥持久化到数据卷
  # (/data/.jwt_secret)，每次重装都注入新值反而会让所有人被登出。
  set -- \
    --name "$CONTAINER_NAME" \
    --restart unless-stopped \
    --label io.singbox-panel.managed=true \
    --log-driver local --log-opt max-size=10m --log-opt max-file=3 \
    -p "127.0.0.1:${PANEL_PORT}:32334" \
    -e "ADMIN=${ADMIN_EMAIL}" \
    -e "ADMIN_PASSWORD=${ADMIN_PASSWORD}" \
    -e "WEB=${WEB}" \
    -v singbox-panel_data:/data
  [ -z "$JWT_SECRET" ] || set -- "$@" -e "JWT_SECRET=${JWT_SECRET}"

  if ! docker run -d "$@" "${IMAGE}:${TAG}" >/dev/null; then
    rollback_docker
    die "容器启动失败"
  fi

  log "等待面板就绪..."
  if ! wait_health "http://127.0.0.1:${PANEL_PORT}/api/health"; then
    err "面板在 60 秒内未就绪，最近日志："
    docker logs --tail=100 "$CONTAINER_NAME" >&2 2>/dev/null || true
    rollback_docker
    die "安装失败"
  fi

  [ "$HAD_PREVIOUS" = "1" ] && docker rm -f "${CONTAINER_NAME}-rollback" >/dev/null 2>&1 || true

  print_docker_done
}

print_docker_done() {
  printf "\n${GREEN}${BOLD}✔ Docker 安装/更新完成！${RESET}\n"
  printf "  面板监听：${BOLD}127.0.0.1:%s${RESET}（仅本机，不对公网开放）\n" "$PANEL_PORT"
  printf "  管理员账号：${BOLD}%s${RESET}\n" "$ADMIN_EMAIL"
  printf "  请配置反向代理（Caddy/Nginx）将 %s 转发到 http://127.0.0.1:%s\n\n" "$WEB" "$PANEL_PORT"
  printf "  ${CYAN}常用命令：${RESET}\n"
  printf "    查看日志：docker logs -f %s\n" "$CONTAINER_NAME"
  printf "    重启面板：docker restart %s\n" "$CONTAINER_NAME"
  printf "    更新面板：重新执行本安装脚本\n\n"
}

###############################################################################
# 探活：30 次 × 2s，失败必须返回非 0（不能装失败还打印成功）
###############################################################################
wait_health() {
  _url="$1"; _i=0
  while [ "$_i" -lt 30 ]; do
    if curl -fsS --max-time 3 "$_url" >/dev/null 2>&1; then
      return 0
    fi
    _i=$((_i+1)); sleep 2
  done
  return 1
}

###############################################################################
# ██████████ BINARY 模式 ██████████
###############################################################################
install_binary() {
  ARCH="$(get_arch)"

  if [ -z "$PANEL_VERSION" ]; then
    log "获取最新版本号..."
    PANEL_VERSION="$(latest_release)"
    [ -n "$PANEL_VERSION" ] || die "无法解析 Release 版本号，请手动指定 PANEL_VERSION=vX.Y.Z"
  fi
  log "版本：${PANEL_VERSION}，架构：${ARCH}"

  DL="https://github.com/${REPO}/releases/download/${PANEL_VERSION}"
  mkdir -p "$INSTALL_DIR" "$DATA_DIR" "$AGENTS_DIR"

  # 校验和清单（缺失不致命，但会提示）
  CHECKSUMS_FILE="$(new_tmp)"
  if curl -fsSL "${DL}/checksums.txt" -o "$CHECKSUMS_FILE" 2>/dev/null; then
    log "已获取 checksums.txt"
  else
    CHECKSUMS_FILE=""
    warn "未能下载 checksums.txt，将跳过完整性校验"
  fi

  log "下载面板二进制..."
  TMP_BIN="$(new_tmp)"
  curl -fsSL "${DL}/singbox-panel-linux-${ARCH}" -o "$TMP_BIN" \
    || die "下载失败：${DL}/singbox-panel-linux-${ARCH}"
  verify_checksum "$TMP_BIN" "singbox-panel-linux-${ARCH}"

  log "下载 Agent 二进制包..."
  TMP_AGENTS="$(new_tmp)"
  _agents_name="singbox-panel-agents-${PANEL_VERSION}.tar.gz"
  curl -fsSL "${DL}/${_agents_name}" -o "$TMP_AGENTS" || die "下载 Agent 包失败：${DL}/${_agents_name}"
  verify_checksum "$TMP_AGENTS" "$_agents_name"
  tar -xzf "$TMP_AGENTS" -C "$AGENTS_DIR" || die "解压 Agent 包失败"
  log "Agent 二进制已解压到 ${AGENTS_DIR}"

  # 前端缺失会导致面板能过健康检查但打不开界面，因此必须视为致命错误。
  WEB_ROOT="${INSTALL_DIR}/web/dist"
  log "下载前端静态文件包..."
  TMP_WEB="$(new_tmp)"
  _web_name="singbox-panel-web-${PANEL_VERSION}.tar.gz"
  curl -fsSL "${DL}/${_web_name}" -o "$TMP_WEB" || die "下载前端包失败：${DL}/${_web_name}"
  verify_checksum "$TMP_WEB" "$_web_name"
  mkdir -p "$WEB_ROOT"
  tar -xzf "$TMP_WEB" -C "$WEB_ROOT" || die "解压前端包失败"
  log "前端静态文件已解压到 ${WEB_ROOT}"

  # 生成配置（已存在则保留用户设置）
  if [ ! -f "$CONF_FILE" ]; then
    ask_domain
    if [ -z "$ADMIN_EMAIL" ]; then
      prompt_read "管理员账号 [默认 admin]："
      ADMIN_EMAIL="${REPLY_VALUE:-admin}"
    fi
    ask_password
    [ -n "$JWT_SECRET" ] || JWT_SECRET="$(rand_hex 32)"

    ( umask 077; cat > "$CONF_FILE" <<EOF
# SingBox Panel 配置文件 — 由安装脚本生成，可手动修改后重启生效

# 仅监听回环地址：面板必须通过反向代理 + HTTPS 域名访问，不直接暴露公网。
listen: "127.0.0.1:${PANEL_PORT}"
base_url: "${WEB}"
jwt_secret: "${JWT_SECRET}"

agents_dir: "${AGENTS_DIR}"
web_dir: "${WEB_ROOT}"

database:
  driver: sqlite
  dsn: "${DATA_DIR}/singbox-panel.db"

admin:
  email: "${ADMIN_EMAIL}"
  password: "${ADMIN_PASSWORD}"
EOF
    )
    chmod 600 "$CONF_FILE"
    log "配置文件已写入 ${CONF_FILE}"
  else
    warn "配置文件已存在，保留现有设置（${CONF_FILE}）"
    _p="$(conf_listen_port || true)"
    [ -z "$_p" ] || PANEL_PORT="$_p"
  fi

  if systemctl is-active --quiet singbox-panel 2>/dev/null; then
    log "停止旧服务..."
    systemctl stop singbox-panel
  fi

  install -m 0755 "$TMP_BIN" "$BIN" || die "安装二进制到 ${BIN} 失败"
  log "面板二进制已安装到 ${BIN}"

  cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=SingBox Panel
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart=${BIN} --config ${CONF_FILE}
Restart=always
RestartSec=5s
LimitNOFILE=infinity
WorkingDirectory=${INSTALL_DIR}

# 面板不需要 root 权限之外的能力，收紧运行环境。
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ProtectKernelTunables=true
ProtectControlGroups=true
ReadWritePaths=${INSTALL_DIR}

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable singbox-panel >/dev/null 2>&1 || true
  systemctl restart singbox-panel

  log "等待面板就绪..."
  if ! wait_health "http://127.0.0.1:${PANEL_PORT}/api/health"; then
    err "面板在 60 秒内未就绪，最近日志："
    journalctl -u singbox-panel -n 100 --no-pager >&2 2>/dev/null || true
    die "安装失败，请检查上方日志"
  fi

  print_binary_done
}

print_binary_done() {
  printf "\n${GREEN}${BOLD}✔ Binary 安装/更新完成！${RESET}\n"
  printf "  安装目录：${BOLD}%s${RESET}\n" "$INSTALL_DIR"
  printf "  配置文件：${BOLD}%s${RESET}\n" "$CONF_FILE"
  printf "  面板监听：${BOLD}127.0.0.1:%s${RESET}（仅本机，不对公网开放）\n" "$PANEL_PORT"
  [ -z "$WEB" ] || printf "  请配置反向代理将 %s 转发到 http://127.0.0.1:%s\n" "$WEB" "$PANEL_PORT"
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
