#!/usr/bin/env bash

set -Eeuo pipefail

DEFAULT_INSTALL_DIR="/opt/singbox-panel"
DEFAULT_PORT="32334"
DEFAULT_ADMIN="admin"

# Binary layout. The panel binary lives on PATH; everything it reads or
# writes stays under INSTALL_DIR so the systemd unit can confine writes there.
GITHUB_REPO="hann0w0/SingBox-Panel"
PANEL_BIN="/usr/local/bin/singbox-panel"
SERVICE_NAME="singbox-panel"
SERVICE_FILE="/etc/systemd/system/singbox-panel.service"

PORT_PROVIDED=0
ADMIN_PROVIDED=0
PASSWORD_PROVIDED=0
BASE_URL_PROVIDED=0
[[ -n "${SINGBOX_PANEL_PORT+x}" ]] && PORT_PROVIDED=1
[[ -n "${SINGBOX_PANEL_ADMIN+x}" ]] && ADMIN_PROVIDED=1
[[ -n "${SINGBOX_PANEL_ADMIN_PASSWORD+x}" ]] && PASSWORD_PROVIDED=1
[[ -n "${SINGBOX_PANEL_BASE_URL+x}" ]] && BASE_URL_PROVIDED=1

INSTALL_DIR="${SINGBOX_PANEL_INSTALL_DIR:-$DEFAULT_INSTALL_DIR}"
PANEL_PORT="${SINGBOX_PANEL_PORT:-$DEFAULT_PORT}"
ADMIN_USERNAME="${SINGBOX_PANEL_ADMIN:-$DEFAULT_ADMIN}"
ADMIN_PASSWORD="${SINGBOX_PANEL_ADMIN_PASSWORD:-}"
BASE_URL="${SINGBOX_PANEL_BASE_URL:-}"
INSTALL_MODE="${SINGBOX_PANEL_MODE:-binary}"
PANEL_VERSION="${SINGBOX_PANEL_VERSION:-}"
NON_INTERACTIVE=0
CONFIGURE=0
PASSWORD_GENERATED=0
ACTION="install"
PURGE_DATA=0
ASSUME_YES=0
INTERACTIVE=0
PROMPT_FD=0
PRESERVED_ROLLBACK_DIR=""
TEMP_FILES=()
TEMP_DIRS=()
JWT_SECRET=""
INSTALL_REAL=""
PANEL_CONFIG=""
CONFIG_WRITTEN=0
BOOTSTRAP_ADMIN_PASSWORD=""

usage() {
  cat <<'EOF'
SingBox Panel installer

Usage:
  sudo ./install.sh [options]
  sudo ./install.sh --uninstall [--purge] [--yes]

Options:
  --mode binary           Backward-compatible explicit binary mode
  --uninstall             Uninstall SingBox Panel; keep database and configuration by default
  --purge, --remove-data  With --uninstall, also delete the database and configuration
  --yes                   Skip the uninstall confirmation prompt
  --port PORT             Local reverse-proxy port (default: 32334)
  --admin USERNAME        Initial administrator username (default: admin)
  --password PASSWORD     Initial administrator password
  --install-dir PATH      Installation directory (default: /opt/singbox-panel)
  --base-url DOMAIN       Required panel/Agent domain (HTTPS)
  --version VERSION       Release tag to install (default: latest)
  --configure             Prompt for settings even when already installed
  --non-interactive       Do not prompt; generate a password when omitted
  -h, --help              Show this help

Environment variable equivalents:
  SINGBOX_PANEL_MODE, SINGBOX_PANEL_PORT, SINGBOX_PANEL_ADMIN,
  SINGBOX_PANEL_ADMIN_PASSWORD, SINGBOX_PANEL_INSTALL_DIR,
  SINGBOX_PANEL_BASE_URL, SINGBOX_PANEL_VERSION

The installer downloads the release binary to /usr/local/bin and runs it under
systemd. Data and configuration live in the installation directory.

The panel binds to 127.0.0.1 only. Put an HTTPS reverse proxy in
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
  local temp_dir=""
  for temp_file in ${TEMP_FILES+"${TEMP_FILES[@]}"}; do
    [[ -n "$temp_file" ]] && rm -f -- "$temp_file"
  done
  for temp_dir in ${TEMP_DIRS+"${TEMP_DIRS[@]}"}; do
    if [[ -n "$temp_dir" && "$temp_dir" != "$PRESERVED_ROLLBACK_DIR" ]]; then
      rm -rf -- "$temp_dir"
    fi
  done
}
trap cleanup EXIT

# mktemp templates must end in the X run: a trailing suffix is kept verbatim by
# GNU coreutils (which then errors) and returned unsubstituted by BSD mktemp.
# Callers that need an extension should rename after the fact.
make_temp() {
  local temp_file=""
  temp_file="$(mktemp "${TMPDIR:-/tmp}/singbox-panel.XXXXXX")" || die "unable to create a temporary file"
  # Command substitution runs this function in a subshell, so mutating the
  # parent TEMP_FILES array here would be misleading. Every caller records the
  # returned path explicitly after assignment.
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

legacy_docker_deployment_exists() {
  [[ -f "$INSTALL_DIR/deploy/.env" ]] && return 0
  if command -v docker >/dev/null 2>&1; then
    local container_name=""
    for container_name in singbox-panel singbox-panel-bootstrap singbox-panel-rollback; do
      if docker container inspect "$container_name" >/dev/null 2>&1; then
        return 0
      fi
    done
    if docker volume inspect singbox-panel_data >/dev/null 2>&1; then
      return 0
    fi
  fi
  return 1
}

[[ "$INSTALL_MODE" == "binary" ]] ||
  die "Docker deployment has been removed; the only supported mode is binary"

if legacy_docker_deployment_exists; then
  if [[ "$ACTION" == "uninstall" && ( -f "$SERVICE_FILE" || -x "$PANEL_BIN" || -f "$INSTALL_DIR/panel.yaml" ) ]]; then
    info "Legacy Docker artifacts detected; uninstalling only the binary deployment"
  else
    die "legacy Docker deployment detected; migrate its data to $INSTALL_DIR/data and remove the old container before using the binary installer"
  fi
fi

# Only the install path needs systemd up front; uninstall degrades gracefully.
if [[ "$ACTION" != "uninstall" ]]; then
  command -v systemctl >/dev/null 2>&1 || die "binary deployment requires systemd"
fi

validate_install_dir() {
  local install_parent=""
  local install_name=""
  [[ "$INSTALL_DIR" == /* ]] || die "install directory must be an absolute path"
  [[ "$INSTALL_DIR" != *[[:space:]]* && "$INSTALL_DIR" != *[[:cntrl:]]* ]] ||
    die "install directory must not contain whitespace or control characters"
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
    printf 'Uninstall SingBox Panel and %s? [y/N]: ' "$detail" >&"$PROMPT_FD"
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

  # A non-purge uninstall must not delete the data directory. Only the
  # redistributable payload goes away.
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

uninstall_singbox_panel() {
  validate_install_dir
  confirm_uninstall
  uninstall_binary_panel
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
EXISTING_INSTALL=0

# Reinstalls keep the existing domain, port and credentials unless the caller
# explicitly overrides them.
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
# The version becomes a path segment in the release download URL. Restrict it
# to release-style tags rather than merely excluding obvious shell syntax.
[[ -z "$PANEL_VERSION" || "$PANEL_VERSION" =~ ^v[0-9]+(\.[0-9]+){1,2}([-+][0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] ||
  die "invalid release version: $PANEL_VERSION (expected vX.Y or vX.Y.Z)"
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

if [[ -z "$ADMIN_PASSWORD" && "$EXISTING_INSTALL" -eq 0 ]]; then
  ADMIN_PASSWORD="$(random_hex 12)"
  PASSWORD_GENERATED=1
fi

username_length="$(LC_ALL=C printf '%s' "$ADMIN_USERNAME" | wc -c | tr -d ' ')"
(( username_length <= 191 )) || die "administrator username must not exceed 191 bytes"
if [[ -n "$ADMIN_PASSWORD" ]]; then
  password_length="$(LC_ALL=C printf '%s' "$ADMIN_PASSWORD" | wc -c | tr -d ' ')"
  (( password_length >= 8 )) || die "administrator password must be at least 8 bytes"
  (( password_length <= 72 )) || die "administrator password must not exceed 72 bytes (bcrypt limit)"
fi
BOOTSTRAP_ADMIN_PASSWORD="$ADMIN_PASSWORD"

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
command -v tar >/dev/null 2>&1 || missing_packages+=("tar")
command -v gzip >/dev/null 2>&1 || missing_packages+=("gzip")
if (( ${#missing_packages[@]} > 0 )); then
  info "Installing required packages: ${missing_packages[*]}"
  install_packages "${missing_packages[@]}"
fi

# The service binds the panel to the configured loopback port.
wait_for_health() {
  local health_url="http://127.0.0.1:${PANEL_PORT}/api/health"
  local ready_url="http://127.0.0.1:${PANEL_PORT}/api/ready"
  for _ in {1..30}; do
    if curl -fsS --max-time 3 "$health_url" >/dev/null 2>&1 &&
       curl -fsS --max-time 3 "$ready_url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  return 1
}

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
  [[ "$expected" =~ ^[[:xdigit:]]{64}$ ]] ||
    die "checksums.txt contains an invalid SHA256 for $asset_name"
  actual="$(sha256_of "$destination")"
  [[ -n "$actual" ]] || die "neither sha256sum nor shasum is available; cannot verify downloads"
  [[ "$actual" =~ ^[[:xdigit:]]{64}$ ]] || die "failed to calculate a valid SHA256 for $asset_name"
  [[ "${expected,,}" == "${actual,,}" ]] ||
    die "SHA256 mismatch for $asset_name (expected $expected, got $actual)"
  info "Verified $asset_name"
}

validate_release_archive() {
  local archive="$1"
  local names_file=""
  local verbose_file=""
  local name=""
  local listing=""
  local entries=0
  local uncompressed_size=""
  names_file="$(mktemp "${TMPDIR:-/tmp}/singbox-panel-archive-names.XXXXXX")" || return 1
  verbose_file="$(mktemp "${TMPDIR:-/tmp}/singbox-panel-archive-types.XXXXXX")" || {
    rm -f -- "$names_file"
    return 1
  }
  if ! tar -tzf "$archive" > "$names_file" || ! tar -tvzf "$archive" > "$verbose_file"; then
    rm -f -- "$names_file" "$verbose_file"
    return 1
  fi
  while IFS= read -r name; do
    entries=$((entries + 1))
    (( entries <= 20000 )) || { rm -f -- "$names_file" "$verbose_file"; return 1; }
    name="${name#./}"
    [[ -z "$name" ]] && continue
    case "$name" in
      /*|..|../*|*/../*|*\\*) rm -f -- "$names_file" "$verbose_file"; return 1 ;;
    esac
    [[ "$name" != *[[:cntrl:]]* ]] || { rm -f -- "$names_file" "$verbose_file"; return 1; }
  done < "$names_file"
  while IFS= read -r listing; do
    case "${listing:0:1}" in
      -|d) ;;
      *) rm -f -- "$names_file" "$verbose_file"; return 1 ;;
    esac
  done < "$verbose_file"
  uncompressed_size="$(gzip -cd -- "$archive" | wc -c | tr -d ' ')" || {
    rm -f -- "$names_file" "$verbose_file"
    return 1
  }
  rm -f -- "$names_file" "$verbose_file"
  [[ "$uncompressed_size" =~ ^[0-9]+$ ]] || return 1
  (( uncompressed_size <= 536870912 ))
}

yaml_double_quote() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  value="${value//$'\t'/\\t}"
  printf '"%s"' "$value"
}

write_panel_config() {
  local config_temp=""
  local listen_yaml="" base_url_yaml="" secret_yaml=""
  local agents_yaml="" web_yaml="" dsn_yaml="" admin_yaml="" password_yaml=""
  # mktemp already creates the file 0600; the explicit chmod after the rename
  # is what guarantees the final mode. No umask change, so nothing leaks into
  # the rest of the run.
  config_temp="$(mktemp "$INSTALL_REAL/panel.yaml.XXXXXX")"
  listen_yaml="$(yaml_double_quote "127.0.0.1:${PANEL_PORT}")"
  base_url_yaml="$(yaml_double_quote "$BASE_URL")"
  secret_yaml="$(yaml_double_quote "$JWT_SECRET")"
  agents_yaml="$(yaml_double_quote "$INSTALL_REAL/dist/agents")"
  web_yaml="$(yaml_double_quote "$INSTALL_REAL/web/dist")"
  dsn_yaml="$(yaml_double_quote "$INSTALL_REAL/data/singbox-panel.db")"
  admin_yaml="$(yaml_double_quote "$ADMIN_USERNAME")"
  password_yaml="$(yaml_double_quote "$ADMIN_PASSWORD")"
  if ! cat >"$config_temp" <<EOF
# Generated by install.sh. Keep this file private and out of Git.
# Edit it and restart with: systemctl restart ${SERVICE_NAME}

# Loopback only: reach the panel through an HTTPS reverse proxy, never directly.
environment: "production"
listen: ${listen_yaml}
base_url: ${base_url_yaml}
jwt_secret: ${secret_yaml}

agents_dir: ${agents_yaml}
web_dir: ${web_yaml}

database:
  driver: sqlite
  dsn: ${dsn_yaml}

# Only used to seed an empty database; ignored once an admin exists.
admin:
  email: ${admin_yaml}
  password: ${password_yaml}
EOF
  then
    rm -f -- "$config_temp"
    return 1
  fi
  mv -f -- "$config_temp" "$PANEL_CONFIG" || return 1
  chown root:root "$PANEL_CONFIG" || return 1
  chmod 600 "$PANEL_CONFIG" || return 1
}

prepare_binary_permissions() {
  install -d -m 700 -o root -g root "$INSTALL_REAL/data" "$INSTALL_REAL/.update" || return 1
  chown -R root:root "$INSTALL_REAL/data" "$INSTALL_REAL/.update" || return 1
  if [[ -f "$PANEL_CONFIG" ]]; then
    chown root:root "$PANEL_CONFIG" || return 1
    chmod 600 "$PANEL_CONFIG" || return 1
  fi
  chown -R root:root "$INSTALL_REAL/dist" "$INSTALL_REAL/web" || return 1
  find "$INSTALL_REAL/dist" "$INSTALL_REAL/web" -type d -exec chmod 755 {} + || return 1
  find "$INSTALL_REAL/web" -type f -exec chmod 644 {} + || return 1
  if [[ -d "$INSTALL_REAL/dist/agents" ]]; then
    find "$INSTALL_REAL/dist/agents" -type f -exec chmod 644 {} + || return 1
    find "$INSTALL_REAL/dist/agents" -type f -name 'singbox-panel-agent-linux-*' -exec chmod 755 {} + || return 1
  fi
}

clear_binary_bootstrap_password() {
  [[ -f "$PANEL_CONFIG" ]] || return 0
  local temp=""
  temp="$(mktemp "$INSTALL_REAL/panel.yaml.scrub.XXXXXX")" || return 1
  if ! awk '
    /^[^[:space:]]/ { in_admin = ($0 ~ /^admin:[[:space:]]*$/) }
    in_admin && /^[[:space:]]+password:[[:space:]]*/ { print "  password: \"\""; next }
    { print }
  ' "$PANEL_CONFIG" > "$temp"; then
    rm -f -- "$temp"
    return 1
  fi
  chown root:root "$temp" || { rm -f -- "$temp"; return 1; }
  chmod 600 "$temp" || { rm -f -- "$temp"; return 1; }
  mv -f -- "$temp" "$PANEL_CONFIG" || { rm -f -- "$temp"; return 1; }
}

write_service_unit() {
  local unit_temp=""
  unit_temp="$(make_temp)"
  TEMP_FILES+=("$unit_temp")
  cat >"$unit_temp" <<EOF
[Unit]
Description=SingBox Panel
Documentation=https://github.com/${GITHUB_REPO}
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
User=root
Group=root
ExecStart=${PANEL_BIN} --config ${PANEL_CONFIG}
Restart=always
RestartSec=5s
LimitNOFILE=infinity
WorkingDirectory=${INSTALL_REAL}
UMask=0077

# Keep the root-running panel confined to its database/update staging area and
# panel.yaml. Privileged maintenance is delegated to short-lived systemd units.
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ProtectKernelTunables=true
ProtectControlGroups=true
ReadWritePaths=${INSTALL_REAL}/data ${INSTALL_REAL}/.update ${PANEL_CONFIG}

[Install]
WantedBy=multi-user.target
EOF
  install -m 644 "$unit_temp" "$SERVICE_FILE" || return 1
  systemctl daemon-reload || return 1
}

install_binary_panel() {
  local arch=""
  local binary_temp=""
  local agents_temp=""
  local web_temp=""
  local checksums_temp=""
  local asset_stage=""
  local rollback_dir=""
  local had_previous_binary=0
  local had_previous_agents=0
  local had_previous_web=0
  local had_previous_unit=0
  local had_previous_config=0
  local data_snapshot_ready=0
  local agents_name=""
  local web_name=""

  arch="$(release_arch)"

  if [[ -z "$PANEL_VERSION" ]]; then
    info "Resolving the latest release"
    PANEL_VERSION="$(latest_release_tag)"
    [[ -n "$PANEL_VERSION" ]] ||
      die "could not parse a release tag from the GitHub API; pass --version vX.Y.Z"
  fi
  [[ "$PANEL_VERSION" =~ ^v[0-9]+(\.[0-9]+){1,2}([-+][0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] ||
    die "GitHub returned an invalid release tag: $PANEL_VERSION"
  info "Installing $PANEL_VERSION for linux/$arch"

  agents_name="singbox-panel-agents-${PANEL_VERSION}.tar.gz"
  web_name="singbox-panel-web-${PANEL_VERSION}.tar.gz"

  install -d -m 755 "$INSTALL_DIR"
  INSTALL_REAL="$(cd -- "$INSTALL_DIR" && pwd -P)"
  PANEL_CONFIG="$INSTALL_REAL/panel.yaml"
  install -d -m 700 "$INSTALL_REAL/data" "$INSTALL_REAL/dist" "$INSTALL_REAL/web"

  checksums_temp="$(make_temp)"
  TEMP_FILES+=("$checksums_temp")
  if curl -fsSL "https://github.com/${GITHUB_REPO}/releases/download/${PANEL_VERSION}/checksums.txt" \
    -o "$checksums_temp"; then
    CHECKSUMS_FILE="$checksums_temp"
  else
    die "unable to download checksums.txt for $PANEL_VERSION; refusing to install unverified assets"
  fi

  binary_temp="$(make_temp)"
  agents_temp="$(make_temp)"
  web_temp="$(make_temp)"
  TEMP_FILES+=("$binary_temp" "$agents_temp" "$web_temp")
  download_release_asset "singbox-panel-linux-${arch}" "$binary_temp"
  download_release_asset "$agents_name" "$agents_temp"
  download_release_asset "$web_name" "$web_temp"

  # Unpack away from the live tree. Nothing currently serving users is touched
  # until every archive has been verified and fully extracted.
  asset_stage="$(mktemp -d "$INSTALL_REAL/.assets.XXXXXX")"
  TEMP_DIRS+=("$asset_stage")
  install -d -m 755 "$asset_stage/agents" "$asset_stage/web"
  validate_release_archive "$agents_temp" || die "$agents_name contains unsafe or oversized archive entries"
  tar -xzf "$agents_temp" -C "$asset_stage/agents" || die "failed to extract $agents_name"
  # A missing frontend still passes /api/health, so the panel would look fine
  # while serving a blank page. Treat an unusable bundle as a failed install.
  validate_release_archive "$web_temp" || die "$web_name contains unsafe or oversized archive entries"
  tar -xzf "$web_temp" -C "$asset_stage/web" || die "failed to extract $web_name"
  if [[ -n "$(find "$asset_stage/agents" "$asset_stage/web" ! -type f ! -type d -print -quit)" ]]; then
    die "release archives created a link or special file"
  fi
  [[ -f "$asset_stage/web/index.html" ]] ||
    die "$web_name did not contain index.html; the frontend bundle is unusable"
  [[ -x "$asset_stage/agents/singbox-panel-agent-linux-amd64" &&
     -x "$asset_stage/agents/singbox-panel-agent-linux-arm64" ]] ||
    die "$agents_name did not contain both executable Agent binaries"

  rollback_dir="$(mktemp -d "$INSTALL_REAL/.rollback.XXXXXX")"
  TEMP_DIRS+=("$rollback_dir")
  if [[ -f "$PANEL_CONFIG" ]]; then
    cp -p -- "$PANEL_CONFIG" "$rollback_dir/config"
    had_previous_config=1
  fi

  if [[ ! -f "$PANEL_CONFIG" || "$CONFIGURE" -eq 1 ]]; then
    # Only mint a secret when one is about to be persisted. Rotating it on an
    # existing install would log every signed-in user out.
    if (( ${#JWT_SECRET} < 24 )); then
      JWT_SECRET="$(random_hex 32)"
    fi
    if ! write_panel_config; then
      if [[ "$had_previous_config" -eq 1 ]]; then
        cp -p -- "$rollback_dir/config" "$PANEL_CONFIG" || true
      else
        rm -f -- "$PANEL_CONFIG"
      fi
      die "failed to write $PANEL_CONFIG"
    fi
    CONFIG_WRITTEN=1
    info "Wrote $PANEL_CONFIG"
  else
    info "Keeping the existing $PANEL_CONFIG (pass --configure to rewrite it)"
  fi

  if [[ -f "$PANEL_BIN" ]]; then
    cp -p -- "$PANEL_BIN" "$rollback_dir/panel"
    had_previous_binary=1
  fi
  if [[ -d "$INSTALL_REAL/dist/agents" ]]; then had_previous_agents=1; fi
  if [[ -d "$INSTALL_REAL/web/dist" ]]; then had_previous_web=1; fi
  if [[ -f "$SERVICE_FILE" ]]; then
    cp -p -- "$SERVICE_FILE" "$rollback_dir/service"
    had_previous_unit=1
  fi

  if [[ "$had_previous_unit" -eq 1 ]]; then
    if ! systemctl stop "$SERVICE_NAME" >/dev/null 2>&1; then
      if [[ "$had_previous_config" -eq 1 ]]; then
        cp -p -- "$rollback_dir/config" "$PANEL_CONFIG" || true
      elif [[ "$CONFIG_WRITTEN" -eq 1 ]]; then
        rm -f -- "$PANEL_CONFIG"
      fi
      die "failed to stop the existing panel before taking a database snapshot"
    fi
  else
    systemctl stop "$SERVICE_NAME" >/dev/null 2>&1 || true
  fi
  if ! cp -a -- "$INSTALL_REAL/data" "$rollback_dir/data"; then
    rm -rf -- "$rollback_dir/data"
    if [[ "$had_previous_config" -eq 1 ]]; then
      cp -p -- "$rollback_dir/config" "$PANEL_CONFIG" || true
    elif [[ "$CONFIG_WRITTEN" -eq 1 ]]; then
      rm -f -- "$PANEL_CONFIG"
    fi
    systemctl restart "$SERVICE_NAME" >/dev/null 2>&1 || true
    die "failed to snapshot the database before replacing the release"
  fi
  data_snapshot_ready=1
  if [[ "$had_previous_agents" -eq 1 ]] && ! mv -- "$INSTALL_REAL/dist/agents" "$rollback_dir/agents"; then
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_agents" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready"
    die "failed to stage the previous Agent bundle"
  fi
  if [[ "$had_previous_web" -eq 1 ]] && ! mv -- "$INSTALL_REAL/web/dist" "$rollback_dir/web"; then
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_agents" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready"
    die "failed to stage the previous frontend bundle"
  fi
  if ! install -d -m 755 "$INSTALL_REAL/dist" "$INSTALL_REAL/web"; then
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_agents" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready"
    die "failed to prepare release directories"
  fi
  if ! mv -- "$asset_stage/agents" "$INSTALL_REAL/dist/agents" ||
     ! mv -- "$asset_stage/web" "$INSTALL_REAL/web/dist" ||
     ! install -m 0755 "$binary_temp" "$PANEL_BIN"; then
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_agents" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready"
    die "failed to install the complete release bundle"
  fi
  if ! prepare_binary_permissions; then
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_agents" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready"
    die "failed to apply secure file permissions"
  fi
  if ! write_service_unit; then
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_agents" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready"
    die "failed to install the systemd service unit"
  fi
  systemctl enable "$SERVICE_NAME" >/dev/null 2>&1 || true

  info "Starting $SERVICE_NAME"
  if ! systemctl restart "$SERVICE_NAME"; then
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_agents" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready"
    die "failed to start $SERVICE_NAME"
  fi

  if ! wait_for_health; then
    printf 'Error: the panel did not become healthy in 60 seconds. Recent logs follow:\n' >&2
    journalctl -u "$SERVICE_NAME" -n 100 --no-pager >&2 2>/dev/null || true
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_agents" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready"
    die "installation failed; see the logs above"
  fi

  if ! clear_binary_bootstrap_password || ! systemctl restart "$SERVICE_NAME" || ! wait_for_health; then
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_agents" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready"
    die "failed to remove the one-time administrator password; the previous installation was restored"
  fi
  rm -rf -- "$rollback_dir" "$asset_stage"
  info "SingBox Panel is healthy"
  print_binary_summary
}

restore_previous_install() {
  local rollback_dir="$1"
  local had_binary="$2"
  local had_agents="$3"
  local had_web="$4"
  local had_unit="$5"
  local had_config="$6"
  local restore_data="$7"
  local failed=0
  printf 'Restoring the previous panel release\n' >&2
  systemctl stop "$SERVICE_NAME" >/dev/null 2>&1 || true
  if [[ -d "$rollback_dir/agents" ]]; then
    if ! rm -rf -- "$INSTALL_REAL/dist/agents" ||
       ! install -d -m 755 "$INSTALL_REAL/dist" ||
       ! mv -- "$rollback_dir/agents" "$INSTALL_REAL/dist/agents"; then
      failed=1
    fi
  elif [[ "$had_agents" -eq 1 ]]; then
    failed=1
  elif [[ "$had_agents" -eq 0 ]]; then
    rm -rf -- "$INSTALL_REAL/dist/agents" || failed=1
  fi
  if [[ -d "$rollback_dir/web" ]]; then
    if ! rm -rf -- "$INSTALL_REAL/web/dist" ||
       ! install -d -m 755 "$INSTALL_REAL/web" ||
       ! mv -- "$rollback_dir/web" "$INSTALL_REAL/web/dist"; then
      failed=1
    fi
  elif [[ "$had_web" -eq 1 ]]; then
    failed=1
  elif [[ "$had_web" -eq 0 ]]; then
    rm -rf -- "$INSTALL_REAL/web/dist" || failed=1
  fi
  if [[ "$had_binary" -eq 1 && -f "$rollback_dir/panel" ]]; then
    install -m 0755 "$rollback_dir/panel" "$PANEL_BIN" || failed=1
  elif [[ "$had_binary" -eq 1 ]]; then
    failed=1
  else
    rm -f -- "$PANEL_BIN" || failed=1
  fi
  if [[ "$had_unit" -eq 1 && -f "$rollback_dir/service" ]]; then
    install -m 0644 "$rollback_dir/service" "$SERVICE_FILE" || failed=1
  elif [[ "$had_unit" -eq 1 ]]; then
    failed=1
  else
    rm -f -- "$SERVICE_FILE" || failed=1
  fi
  if [[ "$had_config" -eq 1 && -f "$rollback_dir/config" ]]; then
    cp -p -- "$rollback_dir/config" "$PANEL_CONFIG" || failed=1
  elif [[ "$had_config" -eq 1 ]]; then
    failed=1
  elif [[ "$CONFIG_WRITTEN" -eq 1 ]]; then
    rm -f -- "$PANEL_CONFIG" || failed=1
  fi
  if [[ "$restore_data" -eq 1 && -d "$rollback_dir/data" ]]; then
    if ! rm -rf -- "$INSTALL_REAL/data" ||
       ! mv -- "$rollback_dir/data" "$INSTALL_REAL/data"; then
      failed=1
    fi
  elif [[ "$restore_data" -eq 1 ]]; then
    failed=1
  fi
  prepare_binary_permissions >/dev/null 2>&1 || failed=1
  systemctl daemon-reload >/dev/null 2>&1 || failed=1
  if [[ "$had_unit" -eq 1 ]]; then
    systemctl restart "$SERVICE_NAME" >/dev/null 2>&1 || failed=1
  fi
  if [[ "$failed" -ne 0 ]]; then
    PRESERVED_ROLLBACK_DIR="$rollback_dir"
    printf 'Automatic rollback was incomplete. Recovery files were preserved at %s\n' "$rollback_dir" >&2
    return 1
  fi
  return 0
}

restore_previous_install_or_die() {
  local rollback_dir="$1"
  if ! restore_previous_install "$@"; then
    die "automatic rollback failed; inspect the preserved recovery files in $rollback_dir"
  fi
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
	printf '  Password:       %s (generated; save it now)\n' "$BOOTSTRAP_ADMIN_PASSWORD"
  else
	printf '  Password:       stored only as a bcrypt hash in the database\n'
  fi
  printf '  Install dir:    %s\n' "$INSTALL_REAL"
  printf '  Configuration:  %s\n' "$PANEL_CONFIG"
  printf '  Binary:         %s\n' "$PANEL_BIN"
  printf '\n  Status:  systemctl status %s\n' "$SERVICE_NAME"
  printf '  Logs:    journalctl -u %s -f\n' "$SERVICE_NAME"
  printf '  Restart: systemctl restart %s\n' "$SERVICE_NAME"
	printf '\nThe one-time bootstrap password was removed from panel.yaml after the database was initialized.\n'
}

install_binary_panel

fi
