#!/usr/bin/env bash

set -Eeuo pipefail

DEFAULT_INSTALL_DIR="/opt/singbox-panel"
DEFAULT_PORT="32334"
DEFAULT_ADMIN="admin"
DEFAULT_IMAGE="hann0w0/singbox-panel"
DEFAULT_TAG="latest"
DEFAULT_MODE="docker"

# Binary-mode layout. The panel binary lives on PATH; everything it reads or
# writes stays under INSTALL_DIR so the systemd unit can confine writes there.
GITHUB_REPO="hann0w0/SingBox-Panel"
PANEL_BIN="/usr/local/bin/singbox-panel"
SERVICE_NAME="singbox-panel"
SERVICE_FILE="/etc/systemd/system/singbox-panel.service"

PORT_PROVIDED=0
ADMIN_PROVIDED=0
PASSWORD_PROVIDED=0
BASE_URL_PROVIDED=0
IMAGE_PROVIDED=0
TAG_PROVIDED=0
MODE_PROVIDED=0
[[ -n "${SINGBOX_PANEL_PORT+x}" ]] && PORT_PROVIDED=1
[[ -n "${SINGBOX_PANEL_ADMIN+x}" ]] && ADMIN_PROVIDED=1
[[ -n "${SINGBOX_PANEL_ADMIN_PASSWORD+x}" ]] && PASSWORD_PROVIDED=1
[[ -n "${SINGBOX_PANEL_BASE_URL+x}" ]] && BASE_URL_PROVIDED=1
[[ -n "${SINGBOX_PANEL_IMAGE+x}" ]] && IMAGE_PROVIDED=1
[[ -n "${SINGBOX_PANEL_TAG+x}" ]] && TAG_PROVIDED=1
[[ -n "${SINGBOX_PANEL_MODE+x}" ]] && MODE_PROVIDED=1

INSTALL_DIR="${SINGBOX_PANEL_INSTALL_DIR:-$DEFAULT_INSTALL_DIR}"
PANEL_PORT="${SINGBOX_PANEL_PORT:-$DEFAULT_PORT}"
ADMIN_USERNAME="${SINGBOX_PANEL_ADMIN:-$DEFAULT_ADMIN}"
ADMIN_PASSWORD="${SINGBOX_PANEL_ADMIN_PASSWORD:-}"
BASE_URL="${SINGBOX_PANEL_BASE_URL:-}"
IMAGE_REPOSITORY="${SINGBOX_PANEL_IMAGE:-$DEFAULT_IMAGE}"
IMAGE_TAG="${SINGBOX_PANEL_TAG:-$DEFAULT_TAG}"
INSTALL_MODE="${SINGBOX_PANEL_MODE:-}"
PANEL_VERSION="${SINGBOX_PANEL_VERSION:-}"
NON_INTERACTIVE=0
SKIP_DOCKER_INSTALL=0
CONFIGURE=0
PASSWORD_GENERATED=0
ACTION="install"
PURGE_DATA=0
ASSUME_YES=0
INTERACTIVE=0
PROMPT_FD=0
PRESERVED_ENV_FILE=""
TEMP_FILES=()
JWT_SECRET=""
INSTALL_REAL=""
PANEL_CONFIG=""
DETECTED_MODE=""
CONFIG_WRITTEN=0

usage() {
  cat <<'EOF'
SingBox Panel installer

Usage:
  sudo ./install.sh [options]
  sudo ./install.sh --uninstall [--purge] [--yes]

Options:
  --mode MODE             Install method: docker (default) or binary
  --uninstall             Uninstall SingBox Panel; keep database and .env by default
  --purge, --remove-data  With --uninstall, also delete the database volume and .env
  --yes                   Skip the uninstall confirmation prompt
  --port PORT             Local reverse-proxy port (default: 32334)
  --admin USERNAME        Initial administrator username (default: admin)
  --password PASSWORD     Initial administrator password
  --install-dir PATH      Installation directory (default: /opt/singbox-panel)
  --base-url DOMAIN       Required panel/Agent domain (HTTPS)
  --image IMAGE           docker mode: image repository (default: hann0w0/singbox-panel)
  --tag TAG               docker mode: image tag (default: latest)
  --version VERSION       binary mode: release tag to install (default: latest)
  --configure             Prompt for settings even when already installed
  --non-interactive       Do not prompt; generate a password when omitted
  --skip-docker-install   Fail instead of installing Docker automatically
  -h, --help              Show this help

Environment variable equivalents:
  SINGBOX_PANEL_MODE, SINGBOX_PANEL_PORT, SINGBOX_PANEL_ADMIN,
  SINGBOX_PANEL_ADMIN_PASSWORD, SINGBOX_PANEL_INSTALL_DIR,
  SINGBOX_PANEL_BASE_URL, SINGBOX_PANEL_IMAGE, SINGBOX_PANEL_TAG,
  SINGBOX_PANEL_VERSION

Install methods:
  docker  Runs the panel in a container; data lives in the singbox-panel_data
          volume. This is the default and the recommended method.
  binary  Downloads a release binary to /usr/local/bin and runs it under
          systemd. Data lives in the install directory. Use this when Docker
          is unavailable. Requires systemd and a published release.

Both methods bind the panel to 127.0.0.1 only. Put an HTTPS reverse proxy in
front of it; the panel is never exposed directly.

The administrator username and password are only used when the database is
empty. Re-running this installer never deletes existing data.

Uninstall keeps the database and configuration by default, so a later install
restores the existing accounts and settings. Add --purge to delete them
permanently. Piped/non-interactive uninstall also requires --yes.
EOF
}

die() {
  printf 'Error: %s\n' "$*" >&2
  exit 1
}

info() {
  printf '[SingBox Panel] %s\n' "$*"
}

cleanup() {
  local temp_file=""
  if [[ -n "$PRESERVED_ENV_FILE" && -f "$PRESERVED_ENV_FILE" ]]; then
    rm -f -- "$PRESERVED_ENV_FILE"
  fi
  for temp_file in ${TEMP_FILES+"${TEMP_FILES[@]}"}; do
    [[ -n "$temp_file" ]] && rm -f -- "$temp_file"
  done
}
trap cleanup EXIT

# mktemp templates must end in the X run: a trailing suffix is kept verbatim by
# GNU coreutils (which then errors) and returned unsubstituted by BSD mktemp.
# Callers that need an extension should rename after the fact.
make_temp() {
  local temp_file=""
  temp_file="$(mktemp "${TMPDIR:-/tmp}/singbox-panel.XXXXXX")" || die "unable to create a temporary file"
  TEMP_FILES+=("$temp_file")
  printf '%s' "$temp_file"
}

need_value() {
  [[ $# -ge 2 && -n "$2" ]] || die "$1 requires a value"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode)
      need_value "$@"
      INSTALL_MODE="$2"
      MODE_PROVIDED=1
      shift 2
      ;;
    --uninstall)
      ACTION="uninstall"
      shift
      ;;
    --purge|--remove-data)
      PURGE_DATA=1
      shift
      ;;
    --yes)
      ASSUME_YES=1
      shift
      ;;
    --port)
      need_value "$@"
      PANEL_PORT="$2"
      PORT_PROVIDED=1
      shift 2
      ;;
    --admin)
      need_value "$@"
      ADMIN_USERNAME="$2"
      ADMIN_PROVIDED=1
      shift 2
      ;;
    --password)
      need_value "$@"
      ADMIN_PASSWORD="$2"
      PASSWORD_PROVIDED=1
      shift 2
      ;;
    --install-dir)
      need_value "$@"
      INSTALL_DIR="$2"
      shift 2
      ;;
    --base-url)
      need_value "$@"
      BASE_URL="$2"
      BASE_URL_PROVIDED=1
      shift 2
      ;;
    --image)
      need_value "$@"
      IMAGE_REPOSITORY="$2"
      IMAGE_PROVIDED=1
      shift 2
      ;;
    --tag)
      need_value "$@"
      IMAGE_TAG="$2"
      TAG_PROVIDED=1
      shift 2
      ;;
    --version)
      need_value "$@"
      PANEL_VERSION="$2"
      shift 2
      ;;
    --configure)
      CONFIGURE=1
      shift
      ;;
    --non-interactive)
      NON_INTERACTIVE=1
      shift
      ;;
    --skip-docker-install)
      SKIP_DOCKER_INSTALL=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown option: $1"
      ;;
  esac
done

[[ ${EUID:-$(id -u)} -eq 0 ]] || die "run this installer as root (for example: sudo ./install.sh)"

# A curl | bash pipeline uses stdin for the script itself, but still has a
# controlling terminal. Open /dev/tty separately so streamed installs can ask
# for configuration without consuming the script input.
if [[ "$NON_INTERACTIVE" -eq 0 ]]; then
  if [[ -t 0 ]]; then
    INTERACTIVE=1
    PROMPT_FD=0
  elif { exec 3<>/dev/tty; } 2>/dev/null; then
    INTERACTIVE=1
    PROMPT_FD=3
  fi
fi

# Which method is already on this machine? Used to pick the uninstall path and
# to keep a reinstall on the same method the user originally chose.
detect_installed_mode() {
  if [[ -f "$SERVICE_FILE" || -x "$PANEL_BIN" ]]; then
    printf 'binary'
    return 0
  fi
  if command -v docker >/dev/null 2>&1 &&
    docker container inspect singbox-panel >/dev/null 2>&1; then
    printf 'docker'
    return 0
  fi
  # A non-purge uninstall deletes the binary and unit but deliberately keeps
  # panel.yaml and the SQLite database. Without this, re-running the installer
  # would fall back to the docker default and strand that database on disk.
  # Docker mode never writes panel.yaml (it uses deploy/.env), so this only
  # matches a binary-mode remnant.
  if [[ -f "$INSTALL_DIR/panel.yaml" ]]; then
    printf 'binary'
    return 0
  fi
  return 1
}

DETECTED_MODE="$(detect_installed_mode || true)"

if [[ -n "$INSTALL_MODE" ]]; then
  case "$INSTALL_MODE" in
    docker|binary) ;;
    *) die "unknown install mode: $INSTALL_MODE (expected docker or binary)" ;;
  esac
  # Installing over the other method would leave the old deployment running and
  # fighting for the same port, so make the user tear it down first. Uninstall
  # is exempt: there the detected method always wins, since that is what is
  # actually on disk.
  if [[ "$ACTION" == "uninstall" ]]; then
    if [[ -n "$DETECTED_MODE" && "$DETECTED_MODE" != "$INSTALL_MODE" ]]; then
      info "Ignoring --mode $INSTALL_MODE; the installed method is $DETECTED_MODE"
      INSTALL_MODE="$DETECTED_MODE"
    fi
  elif [[ -n "$DETECTED_MODE" && "$DETECTED_MODE" != "$INSTALL_MODE" ]]; then
    die "SingBox Panel is already installed in $DETECTED_MODE mode; uninstall it before switching to $INSTALL_MODE mode"
  fi
elif [[ -n "$DETECTED_MODE" ]]; then
  INSTALL_MODE="$DETECTED_MODE"
  [[ "$ACTION" == "uninstall" ]] ||
    info "Detected an existing $INSTALL_MODE-mode installation; keeping that method"
else
  INSTALL_MODE="$DEFAULT_MODE"
fi

# Only the install path needs systemd up front; uninstall degrades gracefully.
if [[ "$INSTALL_MODE" == "binary" && "$ACTION" != "uninstall" ]]; then
  command -v systemctl >/dev/null 2>&1 || die "binary mode requires systemd; use the default docker mode instead"
fi

validate_install_dir() {
  local install_parent=""
  local install_name=""
  [[ "$INSTALL_DIR" == /* ]] || die "install directory must be an absolute path"
  [[ ! -L "$INSTALL_DIR" ]] || die "refusing a symlink install directory: $INSTALL_DIR"
  case "/$INSTALL_DIR/" in
    */../*|*/./*) die "install directory must not contain . or .. path components" ;;
  esac
  # Resolve symlinks using only POSIX shell builtins and pwd -P. GNU
  # `realpath -m --` is unavailable in Alpine/BusyBox even though the installer
  # otherwise supports apk-based systems.
  if [[ -d "$INSTALL_DIR" ]]; then
    INSTALL_DIR="$(cd -- "$INSTALL_DIR" && pwd -P)"
  else
    install_parent="$(dirname -- "$INSTALL_DIR")"
    install_name="$(basename -- "$INSTALL_DIR")"
    if [[ -d "$install_parent" ]]; then
      install_parent="$(cd -- "$install_parent" && pwd -P)"
      INSTALL_DIR="$install_parent/$install_name"
    fi
  fi
  case "$INSTALL_DIR" in
    /|/opt|/usr|/var|/etc|/root|/home)
      die "refusing unsafe install directory: $INSTALL_DIR"
      ;;
  esac
}

confirm_uninstall() {
  [[ "$ASSUME_YES" -eq 1 ]] && return
  if [[ "$INTERACTIVE" -eq 1 ]]; then
    local detail="keep the database and configuration"
    [[ "$PURGE_DATA" -eq 1 ]] && detail="permanently delete the database and configuration"
    local answer=""
    printf 'Uninstall SingBox Panel (%s mode) and %s? [y/N]: ' "$INSTALL_MODE" "$detail" >&"$PROMPT_FD"
    read -r -u "$PROMPT_FD" answer
    [[ "$answer" == "y" || "$answer" == "Y" ]] || {
      info "Uninstall cancelled"
      exit 0
    }
  else
    die "piped/non-interactive uninstall requires --yes"
  fi
}

uninstall_binary_panel() {
  if [[ -f "$SERVICE_FILE" ]]; then
    info "Stopping $SERVICE_NAME"
    systemctl disable --now "$SERVICE_NAME" >/dev/null 2>&1 || true
    rm -f -- "$SERVICE_FILE"
    systemctl daemon-reload || true
    systemctl reset-failed "$SERVICE_NAME" >/dev/null 2>&1 || true
  fi

  if [[ -e "$PANEL_BIN" ]]; then
    info "Removing $PANEL_BIN"
    rm -f -- "$PANEL_BIN"
  fi

  if [[ "$PURGE_DATA" -eq 1 ]]; then
    if [[ -e "$INSTALL_DIR" ]]; then
      info "Removing $INSTALL_DIR"
      find "$INSTALL_DIR" -depth -delete
    fi
    printf '\nSingBox Panel completely uninstalled. Configuration and database were removed.\n'
    return
  fi

  # Unlike docker mode, the SQLite database lives inside INSTALL_DIR rather than
  # a named volume, so a non-purge uninstall must not delete the directory. Only
  # the redistributable payload goes away.
  if [[ -d "$INSTALL_DIR" ]]; then
    rm -rf -- "$INSTALL_DIR/web" "$INSTALL_DIR/dist"
  fi
  printf '\nSingBox Panel uninstalled.\n'
  printf '  Database:       kept at %s/data\n' "$INSTALL_DIR"
  if [[ -f "$INSTALL_DIR/panel.yaml" ]]; then
    printf '  Configuration:  kept at %s/panel.yaml\n' "$INSTALL_DIR"
  fi
  printf 'Run the install command again to restore the panel.\n'
}

uninstall_docker_panel() {
  local docker_ready=0
  local installed_image=""

  if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    docker_ready=1
  fi
  if [[ "$PURGE_DATA" -eq 1 && "$docker_ready" -eq 0 ]]; then
    die "Docker daemon is required for --purge so the database volume can be removed safely"
  fi

  if [[ "$PURGE_DATA" -eq 0 && -f "$INSTALL_DIR/deploy/.env" ]]; then
    PRESERVED_ENV_FILE="$(mktemp)"
    cp -p -- "$INSTALL_DIR/deploy/.env" "$PRESERVED_ENV_FILE"
  fi

  if [[ "$docker_ready" -eq 1 ]]; then
    for container_name in singbox-panel singbox-panel-rollback; do
      if docker container inspect "$container_name" >/dev/null 2>&1; then
        if [[ -z "$installed_image" ]]; then
          installed_image="$(docker container inspect "$container_name" --format '{{.Config.Image}}')"
        fi
        info "Removing SingBox Panel container $container_name"
        docker rm -f "$container_name" >/dev/null
      fi
    done

    for network_name in singbox-panel_default deploy_default; do
      if docker network inspect "$network_name" >/dev/null 2>&1; then
        if [[ "$(docker network inspect "$network_name" --format '{{len .Containers}}')" == "0" ]]; then
          info "Removing SingBox Panel network $network_name"
          docker network rm "$network_name" >/dev/null
        else
          info "Keeping network $network_name because another container still uses it"
        fi
      fi
    done

    if [[ "$PURGE_DATA" -eq 1 ]] && docker volume inspect singbox-panel_data >/dev/null 2>&1; then
      info "Removing SingBox Panel database volume"
      docker volume rm singbox-panel_data >/dev/null
    fi

    for image_name in "$installed_image" hann0w0/singbox-panel:latest ghcr.io/hann0w0/singbox-panel:latest singbox-panel:latest; do
      [[ -n "$image_name" ]] || continue
      if docker image inspect "$image_name" >/dev/null 2>&1; then
        if [[ -z "$(docker ps -aq --filter ancestor="$image_name")" ]]; then
          info "Removing SingBox Panel image $image_name"
          docker image rm "$image_name" >/dev/null
        else
          info "Keeping $image_name because another container still uses it"
        fi
      fi
    done
  fi

  if [[ -e "$INSTALL_DIR" ]]; then
    info "Removing $INSTALL_DIR"
    find "$INSTALL_DIR" -depth -delete
  fi

  if [[ "$PURGE_DATA" -eq 0 ]]; then
    if [[ -n "$PRESERVED_ENV_FILE" && -f "$PRESERVED_ENV_FILE" ]]; then
      install -d -m 700 "$INSTALL_DIR/deploy"
      install -m 600 "$PRESERVED_ENV_FILE" "$INSTALL_DIR/deploy/.env"
    fi
    printf '\nSingBox Panel uninstalled.\n'
    printf '  Database volume: kept (singbox-panel_data)\n'
    if [[ -f "$INSTALL_DIR/deploy/.env" ]]; then
      printf '  Configuration:   kept at %s/deploy/.env\n' "$INSTALL_DIR"
    fi
    printf 'Run the install command again to restore the panel.\n'
  else
    printf '\nSingBox Panel completely uninstalled. Configuration and database volume were removed.\n'
  fi
}

uninstall_singbox_panel() {
  validate_install_dir
  confirm_uninstall
  if [[ "$INSTALL_MODE" == "binary" ]]; then
    uninstall_binary_panel
  else
    uninstall_docker_panel
  fi
}

[[ "$PURGE_DATA" -eq 0 || "$ACTION" == "uninstall" ]] || die "--purge requires --uninstall"
if [[ "$ACTION" == "uninstall" ]]; then
  uninstall_singbox_panel
else

prompt_value() {
  local label="$1"
  local current="$2"
  local answer=""
  printf '%s [%s]: ' "$label" "$current" >&"$PROMPT_FD"
  read -r -u "$PROMPT_FD" answer
  printf '%s' "${answer:-$current}"
}

read_dotenv_value() {
  local key="$1"
  local file="$2"
  local line=""
  local value=""
  line="$(awk -v wanted="$key" 'index($0, wanted "=") == 1 {print; exit}' "$file")"
  [[ -n "$line" ]] || return 1
  value="${line#*=}"
  value="${value%$'\r'}"
  if [[ ${#value} -ge 2 && "${value:0:1}" == "'" && "${value: -1}" == "'" ]]; then
    value="${value:1:${#value}-2}"
    local escaped_quote="\\'"
    local single_quote="'"
    value="${value//$escaped_quote/$single_quote}"
  elif [[ ${#value} -ge 2 && "${value:0:1}" == '"' && "${value: -1}" == '"' ]]; then
    value="${value:1:${#value}-2}"
  fi
  printf '%s' "$value"
}

# Turn the raw text after "key:" into a value. Comments are only stripped from
# unquoted values: a quoted secret or password may legitimately contain '#', and
# truncating it there would silently install the wrong credential.
yaml_clean_value() {
  local value="$1"
  local quote=""
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  quote="${value:0:1}"
  if [[ ${#value} -ge 2 && ( "$quote" == '"' || "$quote" == "'" ) && "$value" == *"$quote"* ]]; then
    # Drop anything after the closing quote (e.g. a trailing comment), then the
    # quotes themselves.
    value="${value%"$quote"*}$quote"
    value="${value:1:${#value}-2}"
  else
    # Unquoted: a '#' starts a comment only after whitespace, or at the start.
    if [[ "$value" == '#'* ]]; then
      value=""
    else
      value="${value%%[[:space:]]#*}"
    fi
    value="${value%"${value##*[![:space:]]}"}"
  fi
  printf '%s' "$value"
}

# Read a scalar from the flat top level of the generated panel.yaml. This is
# deliberately not a YAML parser: it only has to understand the file this
# installer writes, and pulling in a real parser is not worth a dependency.
read_yaml_scalar() {
  local key="$1"
  local file="$2"
  local raw=""
  raw="$(awk -v wanted="$key" '
    index($0, wanted ":") == 1 {
      sub(/^[^:]*:/, "")
      print
      exit
    }' "$file")"
  local value=""
  value="$(yaml_clean_value "$raw")"
  [[ -n "$value" ]] || return 1
  printf '%s' "$value"
}

# Read an indented scalar from inside a top-level block, e.g. admin.password.
# Same narrow scope as read_yaml_scalar: it only parses what this installer writes.
read_yaml_nested() {
  local section="$1"
  local key="$2"
  local file="$3"
  local raw=""
  raw="$(awk -v section="$section" -v wanted="$key" '
    index($0, section ":") == 1 { inside = 1; next }
    # Any other unindented, non-blank, non-comment line ends the block.
    inside && /^[^[:space:]#]/ { inside = 0 }
    inside {
      line = $0
      sub(/^[[:space:]]+/, "", line)
      if (index(line, wanted ":") == 1) {
        sub(/^[^:]*:/, "", line)
        print line
        exit
      }
    }' "$file")"
  local value=""
  value="$(yaml_clean_value "$raw")"
  [[ -n "$value" ]] || return 1
  printf '%s' "$value"
}

PANEL_CONFIG="$INSTALL_DIR/panel.yaml"
EXISTING_ENV="$INSTALL_DIR/deploy/.env"
EXISTING_INSTALL=0

# Reinstalls keep the existing domain, port and credentials unless the caller
# explicitly overrides them.
if [[ "$INSTALL_MODE" == "binary" ]]; then
  if [[ -f "$PANEL_CONFIG" ]]; then
    EXISTING_INSTALL=1
    if [[ "$PORT_PROVIDED" -eq 0 ]]; then
      # listen is "127.0.0.1:32334"; keep only the port.
      existing_listen="$(read_yaml_scalar listen "$PANEL_CONFIG" || true)"
      [[ -z "$existing_listen" ]] || PANEL_PORT="${existing_listen##*:}"
    fi
    if [[ "$BASE_URL_PROVIDED" -eq 0 ]]; then
      BASE_URL="$(read_yaml_scalar base_url "$PANEL_CONFIG" || printf '%s' "$BASE_URL")"
    fi
    # Reuse the stored credentials, otherwise a reinstall would generate a fresh
    # password, keep the old config file, and then report the wrong password.
    if [[ "$ADMIN_PROVIDED" -eq 0 ]]; then
      ADMIN_USERNAME="$(read_yaml_nested admin email "$PANEL_CONFIG" || printf '%s' "$ADMIN_USERNAME")"
    fi
    if [[ "$PASSWORD_PROVIDED" -eq 0 ]]; then
      ADMIN_PASSWORD="$(read_yaml_nested admin password "$PANEL_CONFIG" || printf '%s' "$ADMIN_PASSWORD")"
    fi
    JWT_SECRET="$(read_yaml_scalar jwt_secret "$PANEL_CONFIG" || true)"
  fi
elif [[ -f "$EXISTING_ENV" ]]; then
  EXISTING_INSTALL=1
  if [[ "$PORT_PROVIDED" -eq 0 ]]; then
    PANEL_PORT="$(read_dotenv_value PANEL_PORT "$EXISTING_ENV" || printf '%s' "$PANEL_PORT")"
  fi
  if [[ "$ADMIN_PROVIDED" -eq 0 ]]; then
    ADMIN_USERNAME="$(read_dotenv_value ADMIN "$EXISTING_ENV" || printf '%s' "$ADMIN_USERNAME")"
  fi
  if [[ "$PASSWORD_PROVIDED" -eq 0 ]]; then
    ADMIN_PASSWORD="$(read_dotenv_value ADMIN_PASSWORD "$EXISTING_ENV" || printf '%s' "$ADMIN_PASSWORD")"
  fi
  if [[ "$BASE_URL_PROVIDED" -eq 0 ]]; then
    BASE_URL="$(read_dotenv_value WEB "$EXISTING_ENV" || printf '%s' "$BASE_URL")"
  fi
  if [[ "$IMAGE_PROVIDED" -eq 0 ]]; then
    IMAGE_REPOSITORY="$(read_dotenv_value SINGBOX_PANEL_IMAGE "$EXISTING_ENV" || printf '%s' "$IMAGE_REPOSITORY")"
  fi
  if [[ "$TAG_PROVIDED" -eq 0 ]]; then
    IMAGE_TAG="$(read_dotenv_value SINGBOX_PANEL_TAG "$EXISTING_ENV" || printf '%s' "$IMAGE_TAG")"
  fi
fi

is_ipv4() {
  local address="$1"
  local octet=""
  local -a octets=()
  [[ "$address" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || return 1
  IFS='.' read -r -a octets <<<"$address"
  for octet in "${octets[@]}"; do
    (( 10#$octet <= 255 )) || return 1
  done
  return 0
}

normalize_panel_domain() {
  local domain="$1"
  case "$domain" in
    https://*) domain="${domain#https://}" ;;
    http://*) die "panel domain must use HTTPS" ;;
    *://*) die "panel domain must use HTTPS" ;;
  esac
  domain="${domain%/}"
  [[ -n "$domain" ]] || die "panel and Agent domain is required"
  [[ "$domain" != *[[:space:]]* ]] || die "panel domain cannot contain whitespace"
  [[ "$domain" != */* && "$domain" != *\?* && "$domain" != *\#* && "$domain" != *@* ]] ||
    die "enter a domain only, for example panel.example.com"
  [[ "$domain" != *:* && "$domain" != *\[* && "$domain" != *\]* ]] ||
    die "enter a domain without a port, for example panel.example.com"
  [[ "$domain" != "localhost" ]] || die "localhost cannot be used as the panel domain"
  is_ipv4 "$domain" && die "public IP access is disabled; enter a domain"
  [[ "$domain" =~ ^([A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?\.)+[A-Za-z]([A-Za-z0-9-]*[A-Za-z0-9])?$ ]] ||
    die "invalid panel domain: $domain"
  BASE_URL="https://${domain}"
}

if [[ "$INTERACTIVE" -eq 1 && ( "$EXISTING_INSTALL" -eq 0 || "$CONFIGURE" -eq 1 ) ]]; then
  # Only offer the choice on a genuinely fresh machine. Once either method is
  # installed, DETECTED_MODE has already pinned it and switching needs an
  # explicit uninstall.
  if [[ "$MODE_PROVIDED" -eq 0 && -z "$DETECTED_MODE" ]]; then
    printf '\n请选择安装方式：\n' >&"$PROMPT_FD"
    printf '  1) docker — 容器运行，推荐（默认）\n' >&"$PROMPT_FD"
    printf '  2) binary — 原生二进制 + systemd，适用于无法安装 Docker 的机器\n' >&"$PROMPT_FD"
    mode_answer=""
    printf '输入选项 [1/2，默认 1]: ' >&"$PROMPT_FD"
    read -r -u "$PROMPT_FD" mode_answer
    case "$mode_answer" in
      2|b|binary|Binary) INSTALL_MODE="binary" ;;
      ''|1|d|docker|Docker) INSTALL_MODE="docker" ;;
      *) die "unknown install mode: $mode_answer (expected 1 or 2)" ;;
    esac
    if [[ "$INSTALL_MODE" == "binary" ]]; then
      command -v systemctl >/dev/null 2>&1 ||
        die "binary mode requires systemd; re-run and choose docker"
    fi
    printf '已选择：%s\n\n' "$INSTALL_MODE" >&"$PROMPT_FD"
  fi

  PANEL_PORT="$(prompt_value "面板端口" "$PANEL_PORT")"
  ADMIN_USERNAME="$(prompt_value "管理员账号" "$ADMIN_USERNAME")"

  if [[ -z "$ADMIN_PASSWORD" ]]; then
    printf '管理员密码（明文显示，留空自动生成）: ' >&"$PROMPT_FD"
    read -r -u "$PROMPT_FD" ADMIN_PASSWORD
  fi

  printf '\n提示：安装完成后，请将该域名反向代理到 http://127.0.0.1:%s。\n' "$PANEL_PORT" >&"$PROMPT_FD"
  printf '为避免面板端口暴露公网，SingBox Panel 固定监听本机回环地址，必须通过 HTTPS 域名访问。\n' >&"$PROMPT_FD"
  domain_default="${BASE_URL#https://}"
  domain_default="${domain_default%/}"
  if [[ -n "$domain_default" ]]; then
    BASE_URL="$(prompt_value "面板与 Agent 通信域名" "$domain_default")"
  else
    while [[ -z "$BASE_URL" ]]; do
      printf '面板与 Agent 通信域名（必填，例如 panel.example.com）: ' >&"$PROMPT_FD"
      read -r -u "$PROMPT_FD" BASE_URL
      [[ -n "$BASE_URL" ]] || printf '域名不能为空。\n' >&"$PROMPT_FD"
    done
  fi
fi

[[ -n "$BASE_URL" ]] || die "panel and Agent domain is required; pass --base-url panel.example.com"
normalize_panel_domain "$BASE_URL"

[[ "$PANEL_PORT" =~ ^[0-9]+$ ]] || die "port must be a number"
(( PANEL_PORT >= 1 && PANEL_PORT <= 65535 )) || die "port must be between 1 and 65535"
validate_install_dir
if [[ "$INSTALL_MODE" == "docker" ]]; then
  [[ -n "$IMAGE_REPOSITORY" && "$IMAGE_REPOSITORY" != -* && "$IMAGE_REPOSITORY" != *[[:space:]]* ]] ||
    die "invalid container image repository"
  [[ "$IMAGE_REPOSITORY" != *@* ]] || die "pass an image repository without a digest"
  [[ "$IMAGE_TAG" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]] || die "invalid container image tag"
else
  # The version becomes a path segment in the release download URL.
  [[ -z "$PANEL_VERSION" || "$PANEL_VERSION" =~ ^[A-Za-z0-9_.-]{1,64}$ ]] ||
    die "invalid release version: $PANEL_VERSION"
fi
[[ -n "$ADMIN_USERNAME" ]] || die "administrator username cannot be empty"
[[ "$ADMIN_USERNAME" != *$'\n'* && "$ADMIN_USERNAME" != *$'\r'* ]] || die "administrator username cannot contain a newline"
[[ "$ADMIN_PASSWORD" != *$'\n'* && "$ADMIN_PASSWORD" != *$'\r'* ]] || die "administrator password cannot contain a newline"

random_hex() {
  local bytes="$1"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$bytes"
  else
    od -An -N "$bytes" -tx1 /dev/urandom | tr -d ' \n'
  fi
}

if [[ -z "$ADMIN_PASSWORD" ]]; then
  ADMIN_PASSWORD="$(random_hex 12)"
  PASSWORD_GENERATED=1
fi

password_length="$(LC_ALL=C printf '%s' "$ADMIN_PASSWORD" | wc -c | tr -d ' ')"
(( password_length <= 72 )) || die "administrator password must not exceed 72 bytes (bcrypt limit)"

install_packages() {
  local packages=("$@")
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y "${packages[@]}"
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y "${packages[@]}"
  elif command -v yum >/dev/null 2>&1; then
    yum install -y "${packages[@]}"
  elif command -v apk >/dev/null 2>&1; then
    apk add --no-cache "${packages[@]}"
  else
    die "install curl, then run the installer again"
  fi
}

missing_packages=()
command -v curl >/dev/null 2>&1 || missing_packages+=("curl")
if (( ${#missing_packages[@]} > 0 )); then
  info "Installing required packages: ${missing_packages[*]}"
  install_packages "${missing_packages[@]}"
fi

# Both install methods bind the panel to the same loopback port, so one probe
# serves both. Must be defined before the mode dispatch below.
wait_for_health() {
  local health_url="http://127.0.0.1:${PANEL_PORT}/api/health"
  for _ in {1..30}; do
    if curl -fsS --max-time 3 "$health_url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  return 1
}

ensure_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    [[ "$SKIP_DOCKER_INSTALL" -eq 0 ]] || die "Docker is not installed"
    info "Installing Docker Engine with Docker's official convenience script"
    docker_script="$(mktemp)"
    curl -fsSL https://get.docker.com -o "$docker_script"
    sh "$docker_script"
    rm -f -- "$docker_script"
  fi

  if command -v systemctl >/dev/null 2>&1; then
    systemctl enable --now docker
  fi
  docker info >/dev/null 2>&1 || die "Docker daemon is not available"
}

###############################################################################
# binary mode
###############################################################################

release_arch() {
  case "$(uname -m)" in
    x86_64|amd64) printf 'amd64' ;;
    aarch64|arm64) printf 'arm64' ;;
    *) die "unsupported CPU architecture for binary mode: $(uname -m)" ;;
  esac
}

latest_release_tag() {
  local body=""
  body="$(curl -fsSL "https://api.github.com/repos/${GITHUB_REPO}/releases/latest")" ||
    die "unable to reach the GitHub API (network, or the 60 req/hour anonymous limit). Pass --version vX.Y.Z to skip this lookup"
  # `q` rather than piping to head: under `set -o pipefail` a head that closes
  # the pipe early would SIGPIPE sed and fail the whole command substitution.
  printf '%s' "$body" |
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p;/"tag_name"/q'
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

CHECKSUMS_FILE=""

# Download an asset and check it against checksums.txt. A missing manifest or a
# missing entry is a hard failure: silently installing an unverified binary is
# exactly the outcome the manifest exists to prevent.
download_release_asset() {
  local asset_name="$1"
  local destination="$2"
  local expected=""
  local actual=""

  curl -fsSL "https://github.com/${GITHUB_REPO}/releases/download/${PANEL_VERSION}/${asset_name}" \
    -o "$destination" || die "failed to download $asset_name from release $PANEL_VERSION"

  [[ -n "$CHECKSUMS_FILE" ]] || die "checksums.txt is unavailable; refusing to install unverified assets"
  # sha256sum writes "<hash>  <name>" for text mode and "<hash> *<name>" for
  # binary mode; accept either spelling.
  expected="$(awk -v n="$asset_name" '$2 == n || $2 == "*" n {print $1; exit}' "$CHECKSUMS_FILE")"
  [[ -n "$expected" ]] || die "checksums.txt has no entry for $asset_name; refusing to install it"
  actual="$(sha256_of "$destination")"
  [[ -n "$actual" ]] || die "neither sha256sum nor shasum is available; cannot verify downloads"
  [[ "$expected" == "$actual" ]] ||
    die "SHA256 mismatch for $asset_name (expected $expected, got $actual)"
  info "Verified $asset_name"
}

write_panel_config() {
  local config_temp=""
  # mktemp already creates the file 0600; the explicit chmod after the rename
  # is what guarantees the final mode. No umask change, so nothing leaks into
  # the rest of the run.
  config_temp="$(mktemp "$INSTALL_REAL/panel.yaml.XXXXXX")"
  if ! cat >"$config_temp" <<EOF
# Generated by install.sh. Keep this file private and out of Git.
# Edit it and restart with: systemctl restart ${SERVICE_NAME}

# Loopback only: reach the panel through an HTTPS reverse proxy, never directly.
listen: "127.0.0.1:${PANEL_PORT}"
base_url: "${BASE_URL}"
jwt_secret: "${JWT_SECRET}"

agents_dir: "${INSTALL_REAL}/dist/agents"
web_dir: "${INSTALL_REAL}/web/dist"

database:
  driver: sqlite
  dsn: "${INSTALL_REAL}/data/singbox-panel.db"

# Only used to seed an empty database; ignored once an admin exists.
admin:
  email: "${ADMIN_USERNAME}"
  password: "${ADMIN_PASSWORD}"
EOF
  then
    rm -f -- "$config_temp"
    return 1
  fi
  mv -f -- "$config_temp" "$PANEL_CONFIG"
  chmod 600 "$PANEL_CONFIG"
}

write_service_unit() {
  local unit_temp=""
  unit_temp="$(make_temp)"
  cat >"$unit_temp" <<EOF
[Unit]
Description=SingBox Panel
Documentation=https://github.com/${GITHUB_REPO}
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart=${PANEL_BIN} --config ${PANEL_CONFIG}
Restart=always
RestartSec=5s
LimitNOFILE=infinity
WorkingDirectory=${INSTALL_REAL}

# The panel only ever writes inside its install directory.
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ProtectKernelTunables=true
ProtectControlGroups=true
ReadWritePaths=${INSTALL_REAL}

[Install]
WantedBy=multi-user.target
EOF
  install -m 644 "$unit_temp" "$SERVICE_FILE"
  systemctl daemon-reload
}

install_binary_panel() {
  local arch=""
  local binary_temp=""
  local agents_temp=""
  local web_temp=""
  local checksums_temp=""
  local previous_binary=""
  local agents_name=""
  local web_name=""

  arch="$(release_arch)"

  if [[ -z "$PANEL_VERSION" ]]; then
    info "Resolving the latest release"
    PANEL_VERSION="$(latest_release_tag)"
    [[ -n "$PANEL_VERSION" ]] ||
      die "could not parse a release tag from the GitHub API; pass --version vX.Y.Z"
  fi
  info "Installing $PANEL_VERSION for linux/$arch"

  agents_name="singbox-panel-agents-${PANEL_VERSION}.tar.gz"
  web_name="singbox-panel-web-${PANEL_VERSION}.tar.gz"

  install -d -m 700 "$INSTALL_DIR"
  INSTALL_REAL="$(cd -- "$INSTALL_DIR" && pwd -P)"
  PANEL_CONFIG="$INSTALL_REAL/panel.yaml"
  install -d -m 700 "$INSTALL_REAL/data" "$INSTALL_REAL/dist" "$INSTALL_REAL/web"

  checksums_temp="$(make_temp)"
  if curl -fsSL "https://github.com/${GITHUB_REPO}/releases/download/${PANEL_VERSION}/checksums.txt" \
    -o "$checksums_temp"; then
    CHECKSUMS_FILE="$checksums_temp"
  else
    die "unable to download checksums.txt for $PANEL_VERSION; refusing to install unverified assets"
  fi

  binary_temp="$(make_temp)"
  agents_temp="$(make_temp)"
  web_temp="$(make_temp)"
  download_release_asset "singbox-panel-linux-${arch}" "$binary_temp"
  download_release_asset "$agents_name" "$agents_temp"
  download_release_asset "$web_name" "$web_temp"

  # Unpack into fresh directories so files dropped in a later release do not
  # linger from the previous install.
  rm -rf -- "$INSTALL_REAL/dist/agents" "$INSTALL_REAL/web/dist"
  install -d -m 700 "$INSTALL_REAL/dist/agents" "$INSTALL_REAL/web/dist"
  tar -xzf "$agents_temp" -C "$INSTALL_REAL/dist/agents" || die "failed to extract $agents_name"
  # A missing frontend still passes /api/health, so the panel would look fine
  # while serving a blank page. Treat an unusable bundle as a failed install.
  tar -xzf "$web_temp" -C "$INSTALL_REAL/web/dist" || die "failed to extract $web_name"
  [[ -f "$INSTALL_REAL/web/dist/index.html" ]] ||
    die "$web_name did not contain index.html; the frontend bundle is unusable"

  if [[ ! -f "$PANEL_CONFIG" || "$CONFIGURE" -eq 1 ]]; then
    # Only mint a secret when one is about to be persisted. Rotating it on an
    # existing install would log every signed-in user out.
    if (( ${#JWT_SECRET} < 24 )); then
      JWT_SECRET="$(random_hex 32)"
    fi
    write_panel_config || die "failed to write $PANEL_CONFIG"
    CONFIG_WRITTEN=1
    info "Wrote $PANEL_CONFIG"
  else
    info "Keeping the existing $PANEL_CONFIG (pass --configure to rewrite it)"
  fi

  if [[ -f "$PANEL_BIN" ]]; then
    previous_binary="$(make_temp)"
    cp -p -- "$PANEL_BIN" "$previous_binary"
  fi

  systemctl stop "$SERVICE_NAME" >/dev/null 2>&1 || true
  install -m 0755 "$binary_temp" "$PANEL_BIN" || die "failed to install $PANEL_BIN"
  write_service_unit
  systemctl enable "$SERVICE_NAME" >/dev/null 2>&1 || true

  info "Starting $SERVICE_NAME"
  if ! systemctl restart "$SERVICE_NAME"; then
    restore_previous_binary "$previous_binary"
    die "failed to start $SERVICE_NAME"
  fi

  if ! wait_for_health; then
    printf 'Error: the panel did not become healthy in 60 seconds. Recent logs follow:\n' >&2
    journalctl -u "$SERVICE_NAME" -n 100 --no-pager >&2 2>/dev/null || true
    restore_previous_binary "$previous_binary"
    die "installation failed; see the logs above"
  fi

  info "SingBox Panel is healthy"
  print_binary_summary
}

restore_previous_binary() {
  local previous_binary="$1"
  [[ -n "$previous_binary" && -f "$previous_binary" ]] || return 0
  printf 'Restoring the previous panel binary\n' >&2
  install -m 0755 "$previous_binary" "$PANEL_BIN" || return 0
  systemctl restart "$SERVICE_NAME" >/dev/null 2>&1 || true
}

print_binary_summary() {
  printf '\nInstallation complete.\n'
  printf '  Method:         binary (systemd)\n'
  printf '  Version:        %s\n' "$PANEL_VERSION"
  printf '  URL:            %s\n' "$BASE_URL"
  printf '  Reverse proxy:  http://127.0.0.1:%s\n' "$PANEL_PORT"
  printf '  Administrator:  %s\n' "$ADMIN_USERNAME"
  # Only claim a generated password when this run actually persisted it.
  if [[ "$PASSWORD_GENERATED" -eq 1 && "$CONFIG_WRITTEN" -eq 1 ]]; then
    printf '  Password:       %s (generated; save it now)\n' "$ADMIN_PASSWORD"
  else
    printf '  Password:       stored in %s\n' "$PANEL_CONFIG"
  fi
  printf '  Install dir:    %s\n' "$INSTALL_REAL"
  printf '  Configuration:  %s\n' "$PANEL_CONFIG"
  printf '  Binary:         %s\n' "$PANEL_BIN"
  printf '\n  Status:  systemctl status %s\n' "$SERVICE_NAME"
  printf '  Logs:    journalctl -u %s -f\n' "$SERVICE_NAME"
  printf '  Restart: systemctl restart %s\n' "$SERVICE_NAME"
  printf '\nADMIN and ADMIN_PASSWORD only seed an empty database. Existing accounts are preserved on reinstall.\n'
}

if [[ "$INSTALL_MODE" == "binary" ]]; then
  install_binary_panel
  exit 0
fi

ensure_docker

install -d -m 700 "$INSTALL_DIR/deploy"
INSTALL_REAL="$(cd -- "$INSTALL_DIR" && pwd -P)"
ENV_FILE="$INSTALL_REAL/deploy/.env"
JWT_SECRET=""
if [[ -f "$ENV_FILE" ]]; then
  JWT_SECRET="$(read_dotenv_value JWT_SECRET "$ENV_FILE" || true)"
fi
if (( ${#JWT_SECRET} < 24 )); then
  JWT_SECRET="$(random_hex 32)"
fi

dotenv_write() {
  local key="$1"
  local value="$2"
  local single_quote="'"
  local escaped_quote="\\'"
  value="${value//$single_quote/$escaped_quote}"
  printf "%s='%s'\n" "$key" "$value"
}

write_env_file() {
  local env_temp=""
  umask 077
  env_temp="$(mktemp "$INSTALL_REAL/deploy/.env.XXXXXX")"
  if ! {
    printf '# Generated by install.sh. Keep this file private and out of Git.\n'
    dotenv_write PANEL_PORT "$PANEL_PORT"
    dotenv_write ADMIN "$ADMIN_USERNAME"
    dotenv_write ADMIN_PASSWORD "$ADMIN_PASSWORD"
    dotenv_write WEB "$BASE_URL"
    dotenv_write JWT_SECRET "$JWT_SECRET"
    dotenv_write SINGBOX_PANEL_IMAGE "$IMAGE_REPOSITORY"
    dotenv_write SINGBOX_PANEL_TAG "$IMAGE_TAG"
  } > "$env_temp"; then
    rm -f -- "$env_temp"
    return 1
  fi
  mv -f -- "$env_temp" "$ENV_FILE"
  chmod 600 "$ENV_FILE"
}

remove_legacy_networks() {
  local network_name=""
  for network_name in singbox-panel_default deploy_default; do
    if docker network inspect "$network_name" >/dev/null 2>&1 &&
      [[ "$(docker network inspect "$network_name" --format '{{len .Containers}}')" == "0" ]]; then
      docker network rm "$network_name" >/dev/null || true
    fi
  done
}

start_panel_container() {
  ADMIN="$ADMIN_USERNAME" \
  ADMIN_PASSWORD="$ADMIN_PASSWORD" \
  WEB="$BASE_URL" \
  JWT_SECRET="$JWT_SECRET" \
    docker run -d \
      --name singbox-panel \
      --restart unless-stopped \
      --label io.singbox-panel.managed=true \
      --log-driver local \
      --log-opt max-size=10m \
      --log-opt max-file=3 \
      --publish "127.0.0.1:${PANEL_PORT}:32334" \
      --volume singbox-panel_data:/data \
      --env ADMIN \
      --env ADMIN_PASSWORD \
      --env WEB \
      --env JWT_SECRET \
      "$IMAGE_REF" >/dev/null
}

restore_previous_container() {
  docker rm -f singbox-panel >/dev/null 2>&1 || true
  if [[ "$HAD_PREVIOUS" -eq 1 ]] && docker container inspect singbox-panel-rollback >/dev/null 2>&1; then
    docker rename singbox-panel-rollback singbox-panel
    if [[ "$PREVIOUS_RUNNING" -eq 1 ]]; then
      docker start singbox-panel >/dev/null
    fi
  fi
}

# Recover safely if an earlier installer process was interrupted between the
# rename and health-check steps.
if docker container inspect singbox-panel-rollback >/dev/null 2>&1; then
  if docker container inspect singbox-panel >/dev/null 2>&1; then
    docker rm -f singbox-panel-rollback >/dev/null
  else
    info "Recovering the previous SingBox Panel container"
    docker rename singbox-panel-rollback singbox-panel
    docker start singbox-panel >/dev/null
  fi
fi

IMAGE_REF="${IMAGE_REPOSITORY}:${IMAGE_TAG}"
info "Pulling $IMAGE_REF"
if ! docker pull "$IMAGE_REF"; then
  if [[ "$IMAGE_REPOSITORY" == "$DEFAULT_IMAGE" ]]; then
    IMAGE_REPOSITORY="ghcr.io/hann0w0/singbox-panel"
    IMAGE_REF="${IMAGE_REPOSITORY}:${IMAGE_TAG}"
    info "Docker Hub pull failed; trying $IMAGE_REF"
    docker pull "$IMAGE_REF" || die "unable to pull the SingBox Panel image from Docker Hub or GHCR"
  else
    die "unable to pull $IMAGE_REF"
  fi
fi

docker volume create singbox-panel_data >/dev/null

HAD_PREVIOUS=0
PREVIOUS_RUNNING=0
PREVIOUS_IMAGE_ID=""
if docker container inspect singbox-panel >/dev/null 2>&1; then
  HAD_PREVIOUS=1
  PREVIOUS_IMAGE_ID="$(docker container inspect singbox-panel --format '{{.Image}}')"
  [[ "$(docker container inspect singbox-panel --format '{{.State.Running}}')" == "true" ]] && PREVIOUS_RUNNING=1
  info "Stopping the previous SingBox Panel container"
  docker stop singbox-panel >/dev/null
  docker rename singbox-panel singbox-panel-rollback
fi

info "Starting SingBox Panel from $IMAGE_REF"
if ! start_panel_container; then
  restore_previous_container
  die "failed to create the new SingBox Panel container; the previous container was restored"
fi

if ! wait_for_health; then
  printf 'Error: the new container did not become healthy in 60 seconds. Recent logs follow:\n' >&2
  docker logs --tail=100 singbox-panel >&2 || true
  restore_previous_container
  die "update failed; the previous container was restored"
fi

info "SingBox Panel is healthy"
write_env_file

if [[ "$HAD_PREVIOUS" -eq 1 ]]; then
  docker rm -f singbox-panel-rollback >/dev/null
fi
remove_legacy_networks

CURRENT_IMAGE_ID="$(docker container inspect singbox-panel --format '{{.Image}}')"
if [[ -n "$PREVIOUS_IMAGE_ID" && "$PREVIOUS_IMAGE_ID" != "$CURRENT_IMAGE_ID" ]] &&
  [[ -z "$(docker ps -aq --filter ancestor="$PREVIOUS_IMAGE_ID")" ]]; then
  docker image rm "$PREVIOUS_IMAGE_ID" >/dev/null 2>&1 || true
fi

printf '\nInstallation complete.\n'
printf '  URL:            %s\n' "$BASE_URL"
printf '  Reverse proxy:  http://127.0.0.1:%s\n' "$PANEL_PORT"
printf '  Administrator:  %s\n' "$ADMIN_USERNAME"
if [[ "$PASSWORD_GENERATED" -eq 1 ]]; then
  printf '  Password:       %s (generated; save it now)\n' "$ADMIN_PASSWORD"
else
  printf '  Password:       stored in %s\n' "$ENV_FILE"
fi
printf '  Install dir:    %s\n' "$INSTALL_REAL"
printf '  Configuration:  %s\n' "$ENV_FILE"
printf '\nADMIN and ADMIN_PASSWORD only seed an empty database. Existing accounts are preserved on reinstall.\n'

fi
