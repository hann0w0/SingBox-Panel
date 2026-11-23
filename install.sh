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
PRIVATE_TMP_DIR=""
LOCK_FILE=""
LOCK_DIR=""
LOCK_HELD=0
TEMP_FILES=()
TEMP_DIRS=()
JWT_SECRET=""
INSTALL_REAL=""
PANEL_CONFIG=""
CONFIG_WRITTEN=0
BOOTSTRAP_PASSWORD_WRITTEN=0
BOOTSTRAP_ADMIN_PASSWORD=""
VERIFY_ATTESTATION="${SINGBOX_PANEL_VERIFY_ATTESTATION:-0}"
PASSWORD_STDIN=0
PASSWORD_FILE=""
CURL_DOWNLOAD_ARGS=(--fail --silent --show-error --location --connect-timeout 10 --max-time 300 --retry 3 --retry-delay 2 --retry-connrefused)

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
  --password PASSWORD     Initial administrator password (deprecated; exposes password in ps)
  --password-stdin        Read initial administrator password from stdin
  --password-file PATH    Read initial administrator password from a file
  --install-dir PATH      Installation directory (default: /opt/singbox-panel)
  --base-url DOMAIN       Required panel/Agent domain (HTTPS)
  --version VERSION       Release tag to install (default: latest)
  --verify-attestation    Verify checksums.txt SLSA provenance via GitHub CLI (gh)
  --configure             Prompt for settings even when already installed
  --non-interactive       Do not prompt; generate a password when omitted
  -h, --help              Show this help

Environment variable equivalents:
  SINGBOX_PANEL_MODE, SINGBOX_PANEL_PORT, SINGBOX_PANEL_ADMIN,
  SINGBOX_PANEL_ADMIN_PASSWORD, SINGBOX_PANEL_INSTALL_DIR,
  SINGBOX_PANEL_BASE_URL, SINGBOX_PANEL_VERSION,
  SINGBOX_PANEL_VERIFY_ATTESTATION

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
  # Never leave a plaintext bootstrap password behind if installation is aborted
  # after panel.yaml was written but before healthcheck scrubbing completed.
  if [[ "$BOOTSTRAP_PASSWORD_WRITTEN" -eq 1 && -n "$PANEL_CONFIG" && -f "$PANEL_CONFIG" ]]; then
    if declare -F clear_binary_bootstrap_password >/dev/null 2>&1; then
      clear_binary_bootstrap_password >/dev/null 2>&1 || true
    fi
    BOOTSTRAP_PASSWORD_WRITTEN=0
  fi
  if (( ${#TEMP_FILES[@]} > 0 )); then
    for temp_file in "${TEMP_FILES[@]}"; do
      [[ -n "$temp_file" ]] && rm -f -- "$temp_file"
    done
  fi
  if (( ${#TEMP_DIRS[@]} > 0 )); then
    for temp_dir in "${TEMP_DIRS[@]}"; do
      if [[ -n "$temp_dir" && "$temp_dir" != "$PRESERVED_ROLLBACK_DIR" ]]; then
        rm -rf -- "$temp_dir"
      fi
    done
  fi
  if [[ -n "$PRIVATE_TMP_DIR" && -d "$PRIVATE_TMP_DIR" ]]; then
    rm -rf -- "$PRIVATE_TMP_DIR"
  fi
  if [[ "$LOCK_HELD" -eq 1 ]]; then
    if [[ -n "$LOCK_DIR" ]]; then
      rm -rf -- "$LOCK_DIR"
    fi
    if [[ -n "$LOCK_FILE" ]]; then
      exec 9>&- 2>/dev/null || true
    fi
    LOCK_HELD=0
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Ignore untrusted caller-supplied TMPDIR (e.g. via sudo -E) and create a
# private 0700 workspace under /tmp for all installer temporary files.
ensure_private_tmp() {
  if [[ -n "$PRIVATE_TMP_DIR" && -d "$PRIVATE_TMP_DIR" ]]; then
    return 0
  fi
  PRIVATE_TMP_DIR="$(mktemp -d "/tmp/singbox-panel-tmp.XXXXXX")" || die "unable to create a private temporary directory"
  chmod 700 "$PRIVATE_TMP_DIR" || die "unable to secure temporary directory"
}

# mktemp templates must end in the X run: a trailing suffix is kept verbatim by
# GNU coreutils (which then errors) and returned unsubstituted by BSD mktemp.
# Callers that need an extension should rename after the fact.
make_temp() {
  local temp_file=""
  [[ -n "$PRIVATE_TMP_DIR" && -d "$PRIVATE_TMP_DIR" ]] || die "private temporary directory not initialized"
  temp_file="$(mktemp "${PRIVATE_TMP_DIR}/singbox-panel.XXXXXX")" || die "unable to create a temporary file"
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
      printf 'Warning: --password exposes the password in process listings; prefer --password-stdin, --password-file, or SINGBOX_PANEL_ADMIN_PASSWORD\n' >&2
      ADMIN_PASSWORD="$2"
      PASSWORD_PROVIDED=1
      shift 2
      ;;
    --password-stdin)
      PASSWORD_STDIN=1
      PASSWORD_PROVIDED=1
      shift
      ;;
    --password-file)
      need_value "$@"
      PASSWORD_FILE="$2"
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
    --verify-attestation)
      VERIFY_ATTESTATION=1
      shift
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

if [[ "$PASSWORD_STDIN" -eq 1 && -n "$PASSWORD_FILE" ]]; then
  die "--password-stdin and --password-file cannot be combined"
fi
if [[ "$PASSWORD_STDIN" -eq 1 ]]; then
  IFS= read -r ADMIN_PASSWORD || [[ -n "$ADMIN_PASSWORD" ]] || die "failed to read administrator password from stdin"
  ADMIN_PASSWORD="${ADMIN_PASSWORD%$'\r'}"
elif [[ -n "$PASSWORD_FILE" ]]; then
  [[ -r "$PASSWORD_FILE" ]] || die "cannot read password file: $PASSWORD_FILE"
  IFS= read -r ADMIN_PASSWORD < "$PASSWORD_FILE" || [[ -n "$ADMIN_PASSWORD" ]] || die "password file is empty: $PASSWORD_FILE"
  ADMIN_PASSWORD="${ADMIN_PASSWORD%$'\r'}"
fi

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

acquire_install_lock() {
  local lock_base="/tmp"
  if [[ -d /run && -w /run ]]; then
    lock_base="/run"
  fi
  local dir_hash=""
  dir_hash="$(printf '%s' "$INSTALL_DIR" | cksum | awk '{print $1}')"
  if command -v flock >/dev/null 2>&1; then
    LOCK_FILE="${lock_base}/singbox-panel-install-${dir_hash}.lock"
    exec 9>"$LOCK_FILE" || die "unable to open installer lock file: $LOCK_FILE"
    flock -n 9 || die "another SingBox Panel install/uninstall process is already running"
    LOCK_HELD=1
  else
    LOCK_DIR="${lock_base}/singbox-panel-install-${dir_hash}.lock.d"
    if ! mkdir "$LOCK_DIR" 2>/dev/null; then
      local existing_pid=""
      existing_pid="$(cat "$LOCK_DIR/pid" 2>/dev/null || true)"
      if [[ -n "$existing_pid" ]] && kill -0 "$existing_pid" 2>/dev/null; then
        die "another SingBox Panel install/uninstall process (PID $existing_pid) is already running"
      fi
      rm -rf -- "$LOCK_DIR"
      mkdir "$LOCK_DIR" 2>/dev/null || die "another SingBox Panel install/uninstall process is already running"
    fi
    chmod 700 "$LOCK_DIR" 2>/dev/null || true
    printf '%s\n' "$$" > "$LOCK_DIR/pid"
    LOCK_HELD=1
  fi

  # Also serialize against an in-progress panel self-update detached helper.
  local active_update_marker="$INSTALL_DIR/.update/.active"
  if [[ -f "$active_update_marker" ]]; then
    local now_ts="" marker_ts=""
    now_ts="$(date +%s)"
    marker_ts="$(stat -c %Y "$active_update_marker" 2>/dev/null || stat -f %m "$active_update_marker" 2>/dev/null || printf '0')"
    if [[ "$marker_ts" =~ ^[0-9]+$ ]] && (( now_ts - marker_ts <= 1800 )); then
      die "a panel self-update is currently in progress ($active_update_marker); wait for it to finish before running install.sh"
    fi
  fi
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

panel_process_running() {
  if command -v pgrep >/dev/null 2>&1; then
    pgrep -x singbox-panel >/dev/null 2>&1 && return 0
    pgrep -f '(^|/)singbox-panel([[:space:]]|$)' >/dev/null 2>&1 && return 0
  fi
  return 1
}

uninstall_binary_panel() {
  if [[ -f "$SERVICE_FILE" ]]; then
    info "Stopping $SERVICE_NAME"
    systemctl disable --now "$SERVICE_NAME" >/dev/null 2>&1 || true
    if systemctl is-active --quiet "$SERVICE_NAME" || panel_process_running; then
      die "refusing to uninstall while singbox-panel is still running"
    fi
    rm -f -- "$SERVICE_FILE"
    systemctl daemon-reload || true
    systemctl reset-failed "$SERVICE_NAME" >/dev/null 2>&1 || true
  fi

  if panel_process_running; then
    die "refusing to remove files while singbox-panel is still running"
  fi

  if [[ -e "$PANEL_BIN" ]]; then
    info "Removing $PANEL_BIN"
    rm -f -- "$PANEL_BIN"
  fi

  if [[ "$PURGE_DATA" -eq 1 ]]; then
    if [[ -e "$INSTALL_DIR" ]]; then
      info "Removing $INSTALL_DIR"
      find "$INSTALL_DIR" -depth -delete || die "failed to completely remove $INSTALL_DIR"
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
  if [[ -d "$INSTALL_DIR/backups" ]]; then
    printf '  Backups:        kept at %s/backups\n' "$INSTALL_DIR"
  fi
  if [[ -d "$INSTALL_DIR/.update" ]]; then
    printf '  Update state:   kept at %s/.update\n' "$INSTALL_DIR"
  fi
  local rollback_match=""
  for rollback_match in "$INSTALL_DIR"/.rollback.*; do
    if [[ -e "$rollback_match" ]]; then
      printf '  Rollback dir:   kept at %s\n' "$rollback_match"
    fi
  done
  printf 'Run the install command again to restore the panel (or re-run with --uninstall --purge to remove all kept files).\n'
}

uninstall_singbox_panel() {
  validate_install_dir
  acquire_install_lock
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
  if [[ ${#value} -ge 2 && "$quote" == '"' ]]; then
    local out="" i=1 ch="" next="" closed=0
    while (( i < ${#value} )); do
      ch="${value:i:1}"
      if [[ "$ch" == '\' && $((i + 1)) -lt ${#value} ]]; then
        next="${value:i+1:1}"
        case "$next" in
          t) out+=$'\t' ;;
          n) out+=$'\n' ;;
          r) out+=$'\r' ;;
          *) out+="$next" ;;
        esac
        i=$((i + 2))
        continue
      fi
      if [[ "$ch" == '"' ]]; then
        closed=1
        break
      fi
      out+="$ch"
      i=$((i + 1))
    done
    if (( closed )); then
      printf '%s' "$out"
      return 0
    fi
  elif [[ ${#value} -ge 2 && "$quote" == "'" && "${value:1}" == *"'"* ]]; then
    value="${value%"'"*}'"
    value="${value:1:${#value}-2}"
    printf '%s' "$value"
    return 0
  fi
  # Unquoted: a '#' starts a comment only after whitespace, or at the start.
  if [[ "$value" == '#'* ]]; then
    value=""
  else
    value="${value%%[[:space:]]#*}"
  fi
  value="${value%"${value##*[![:space:]]}"}"
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

# Normalize and vet the install directory before anything is read from it:
# the existing panel.yaml supplies credentials and the JWT secret below.
validate_install_dir
acquire_install_lock
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
    printf '管理员密码（不回显，留空自动生成）: ' >&"$PROMPT_FD"
    read -rs -u "$PROMPT_FD" ADMIN_PASSWORD
    printf '\n' >&"$PROMPT_FD"
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
if [[ "$EXISTING_INSTALL" -eq 0 || "$CONFIGURE" -eq 1 || "$BASE_URL_PROVIDED" -eq 1 ]]; then
  normalize_panel_domain "$BASE_URL"
fi

[[ "$PANEL_PORT" =~ ^[0-9]+$ ]] || die "port must be a number"
(( PANEL_PORT >= 1 && PANEL_PORT <= 65535 )) || die "port must be between 1 and 65535"
# The version becomes a path segment in the release download URL. Restrict it
# to release-style tags rather than merely excluding obvious shell syntax.
[[ -z "$PANEL_VERSION" || "$PANEL_VERSION" =~ ^v[0-9]+(\.[0-9]+){1,2}([-+][0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] ||
  die "invalid release version: $PANEL_VERSION (expected vX.Y or vX.Y.Z)"
[[ -n "$ADMIN_USERNAME" ]] || die "administrator username cannot be empty"
[[ "$ADMIN_USERNAME" != *[[:cntrl:]]* ]] || die "administrator username cannot contain control characters"
[[ "$ADMIN_PASSWORD" != *$'\n'* && "$ADMIN_PASSWORD" != *$'\r'* ]] || die "administrator password cannot contain a newline"

random_hex() {
  local bytes="$1"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$bytes"
  else
    od -An -N "$bytes" -tx1 /dev/urandom | tr -d ' \n'
  fi
}

if [[ -z "$ADMIN_PASSWORD" && ( "$EXISTING_INSTALL" -eq 0 || "$CONFIGURE" -eq 1 ) ]]; then
  ADMIN_PASSWORD="$(random_hex 12)"
  PASSWORD_GENERATED=1
fi

username_length="$(LC_ALL=C printf '%s' "$ADMIN_USERNAME" | wc -c | tr -d ' ')"
(( username_length <= 191 )) || die "administrator username must not exceed 191 bytes"
if [[ -n "$ADMIN_PASSWORD" ]]; then
  password_length="$(LC_ALL=C printf '%s' "$ADMIN_PASSWORD" | wc -c | tr -d ' ')"
  (( password_length >= 1 )) || die "administrator password cannot be empty"
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
  body="$(curl "${CURL_DOWNLOAD_ARGS[@]}" "https://api.github.com/repos/${GITHUB_REPO}/releases/latest")" ||
    die "unable to reach the GitHub API (network, or the 60 req/hour anonymous limit). Pass --version vX.Y.Z to skip this lookup"
  # Feed $body via a here-string so `sed ... q` never closes a pipe early and
  # triggers SIGPIPE under `set -o pipefail` on large release payloads.
  sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p;/"tag_name"/q' <<<"$body"
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

  curl "${CURL_DOWNLOAD_ARGS[@]}" "https://github.com/${GITHUB_REPO}/releases/download/${PANEL_VERSION}/${asset_name}" \
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
  expected="$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')"
  actual="$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')"
  [[ "$expected" == "$actual" ]] ||
    die "SHA256 mismatch for $asset_name (expected $expected, got $actual)"
  info "Verified $asset_name"
}

verify_checksums_attestation() {
  local checksums_path="$1"
  case "$VERIFY_ATTESTATION" in
    1|true|yes|require) ;;
    *) return 0 ;;
  esac
  command -v gh >/dev/null 2>&1 ||
    die "--verify-attestation requires the GitHub CLI (gh) to verify SLSA build provenance"
  gh attestation verify "$checksums_path" --repo "$GITHUB_REPO" >/dev/null ||
    die "SLSA build provenance verification failed for checksums.txt"
  info "Verified SLSA build provenance for checksums.txt"
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
  local dsn_yaml="" admin_yaml="" password_yaml=""
  # mktemp already creates the file 0600; the explicit chmod after the rename
  # is what guarantees the final mode. No umask change, so nothing leaks into
  # the rest of the run.
  config_temp="$(mktemp "$INSTALL_REAL/panel.yaml.XXXXXX")"
  listen_yaml="$(yaml_double_quote "127.0.0.1:${PANEL_PORT}")"
  base_url_yaml="$(yaml_double_quote "$BASE_URL")"
  secret_yaml="$(yaml_double_quote "$JWT_SECRET")"
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

# Remove obsolete default agents_dir/web_dir lines from legacy installs so the
# upgraded config reflects the single-binary layout without touching custom paths.
prune_legacy_panel_asset_config() {
  [[ -f "$PANEL_CONFIG" ]] || return 0
  local config_temp=""
  config_temp="$(mktemp "$INSTALL_REAL/panel.yaml.XXXXXX")" || return 1
  if ! SB_INSTALL_REAL="$INSTALL_REAL" SB_INSTALL_DIR="${INSTALL_DIR%/}" awk '
    function unquote(v) {
      sub(/^[[:space:]]+/, "", v)
      sub(/[[:space:]]+$/, "", v)
      if ((substr(v, 1, 1) == "\"" && substr(v, length(v), 1) == "\"") ||
          (substr(v, 1, 1) == "'"'"'" && substr(v, length(v), 1) == "'"'"'")) {
        v = substr(v, 2, length(v) - 2)
      }
      return v
    }
    function is_legacy_path(v, root) {
      if (root == "") return 0
      return (v == root "/dist/agents" || v == root "/web/dist" ||
              v == root "/agents" || v == root "/web")
    }
    /^(agents_dir|web_dir)[[:space:]]*:/ {
      val = $0
      sub(/^[^:]+:[[:space:]]*/, "", val)
      val = unquote(val)
      if (is_legacy_path(val, ENVIRON["SB_INSTALL_REAL"]) ||
          is_legacy_path(val, ENVIRON["SB_INSTALL_DIR"])) {
        next
      }
    }
    { print }
  ' "$PANEL_CONFIG" > "$config_temp"; then
    rm -f -- "$config_temp"
    return 1
  fi
  chown root:root "$config_temp" && chmod 600 "$config_temp" && mv -f -- "$config_temp" "$PANEL_CONFIG" || {
    rm -f -- "$config_temp"
    return 1
  }
}

# Explicit endpoint overrides must be persisted without replacing unrelated
# database, JWT, static asset or administrator settings in an existing config.
update_panel_endpoint_config() {
  local config_temp=""
  config_temp="$(mktemp "$INSTALL_REAL/panel.yaml.XXXXXX")" || return 1
  if ! SB_INSTALL_CHANGE_PORT="$PORT_PROVIDED" \
    SB_INSTALL_CHANGE_ORIGIN="$BASE_URL_PROVIDED" \
    SB_INSTALL_LISTEN="$(yaml_double_quote "127.0.0.1:${PANEL_PORT}")" \
    SB_INSTALL_ORIGIN="$(yaml_double_quote "$BASE_URL")" \
    awk '
      BEGIN {
        change_port = ENVIRON["SB_INSTALL_CHANGE_PORT"] == "1"
        change_origin = ENVIRON["SB_INSTALL_CHANGE_ORIGIN"] == "1"
      }
      /^listen[[:space:]]*:/ && change_port {
        if (!port_written++) print "listen: " ENVIRON["SB_INSTALL_LISTEN"]
        next
      }
      /^base_url[[:space:]]*:/ && change_origin {
        if (!origin_written++) print "base_url: " ENVIRON["SB_INSTALL_ORIGIN"]
        next
      }
      { print }
      END {
        if (change_port && !port_written) print "listen: " ENVIRON["SB_INSTALL_LISTEN"]
        if (change_origin && !origin_written) print "base_url: " ENVIRON["SB_INSTALL_ORIGIN"]
      }
    ' "$PANEL_CONFIG" > "$config_temp"; then
    rm -f -- "$config_temp"
    return 1
  fi
  chown root:root "$config_temp" && chmod 600 "$config_temp" && mv -f -- "$config_temp" "$PANEL_CONFIG" || {
    rm -f -- "$config_temp"
    return 1
  }
}

prepare_binary_permissions() {
  install -d -m 700 -o root -g root "$INSTALL_REAL/data" "$INSTALL_REAL/.update" || return 1
  chown -R root:root "$INSTALL_REAL/data" "$INSTALL_REAL/.update" || return 1
  if [[ -f "$PANEL_CONFIG" ]]; then
    chown root:root "$PANEL_CONFIG" || return 1
    chmod 600 "$PANEL_CONFIG" || return 1
  fi
  if [[ -d "$INSTALL_REAL/dist" ]]; then
    chown -R root:root "$INSTALL_REAL/dist" || return 1
    find "$INSTALL_REAL/dist" -type d -exec chmod 755 {} + || return 1
    if [[ -d "$INSTALL_REAL/dist/agents" ]]; then
      find "$INSTALL_REAL/dist/agents" -type f -exec chmod 644 {} + || return 1
      find "$INSTALL_REAL/dist/agents" -type f -name 'singbox-panel-agent-linux-*' -exec chmod 755 {} + || return 1
    fi
  fi
  if [[ -d "$INSTALL_REAL/web" ]]; then
    chown -R root:root "$INSTALL_REAL/web" || return 1
    find "$INSTALL_REAL/web" -type d -exec chmod 755 {} + || return 1
    find "$INSTALL_REAL/web" -type f -exec chmod 644 {} + || return 1
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
PrivateDevices=true
ProtectSystem=full
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectClock=true
LockPersonality=true
RestrictRealtime=true
RestrictSUIDSGID=true
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
  local checksums_temp=""
  local rollback_dir=""
  local had_previous_binary=0
  local had_previous_dist=0
  local had_previous_web=0
  local had_previous_unit=0
  local had_previous_config=0
  local service_enabled_by_install=0
  local data_snapshot_ready=0

  ensure_private_tmp
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

  install -d -m 755 "$INSTALL_DIR"
  INSTALL_REAL="$(cd -- "$INSTALL_DIR" && pwd -P)"
  PANEL_CONFIG="$INSTALL_REAL/panel.yaml"
  install -d -m 700 "$INSTALL_REAL/data"

  checksums_temp="$(make_temp)"
  TEMP_FILES+=("$checksums_temp")
  if curl "${CURL_DOWNLOAD_ARGS[@]}" "https://github.com/${GITHUB_REPO}/releases/download/${PANEL_VERSION}/checksums.txt" \
    -o "$checksums_temp"; then
    verify_checksums_attestation "$checksums_temp"
    CHECKSUMS_FILE="$checksums_temp"
  else
    die "unable to download checksums.txt for $PANEL_VERSION; refusing to install unverified assets"
  fi

  binary_temp="$(make_temp)"
  TEMP_FILES+=("$binary_temp")
  download_release_asset "singbox-panel-linux-${arch}" "$binary_temp"

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
    [[ -n "$ADMIN_PASSWORD" ]] && BOOTSTRAP_PASSWORD_WRITTEN=1
    info "Wrote $PANEL_CONFIG"
  elif [[ "$PORT_PROVIDED" -eq 1 || "$BASE_URL_PROVIDED" -eq 1 ]]; then
    if ! update_panel_endpoint_config; then
      cp -p -- "$rollback_dir/config" "$PANEL_CONFIG" || true
      die "failed to update the panel port or domain in $PANEL_CONFIG"
    fi
    CONFIG_WRITTEN=1
    info "Updated the requested port/domain in $PANEL_CONFIG; other settings were preserved"
  else
    info "Keeping the existing $PANEL_CONFIG (pass --configure to rewrite it)"
  fi

  if [[ "$had_previous_config" -eq 1 && "$CONFIGURE" -eq 0 ]]; then
    if ! prune_legacy_panel_asset_config; then
      cp -p -- "$rollback_dir/config" "$PANEL_CONFIG" || true
      die "failed to clean legacy web_dir/agents_dir entries in $PANEL_CONFIG"
    fi
  fi

  if [[ -f "$PANEL_BIN" ]]; then
    cp -p -- "$PANEL_BIN" "$rollback_dir/panel"
    had_previous_binary=1
  fi
  if [[ -d "$INSTALL_REAL/dist" ]]; then had_previous_dist=1; fi
  if [[ -d "$INSTALL_REAL/web" ]]; then had_previous_web=1; fi
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

  # Move legacy on-disk frontend and Agent directories into the rollback bundle
  # so they are restored on failure and completely deleted once the single
  # binary passes health checks.
  if [[ "$had_previous_dist" -eq 1 ]] && ! mv -- "$INSTALL_REAL/dist" "$rollback_dir/dist"; then
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_dist" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready" "$service_enabled_by_install"
    die "failed to stage legacy Agent directory for cleanup"
  fi
  if [[ "$had_previous_web" -eq 1 ]] && ! mv -- "$INSTALL_REAL/web" "$rollback_dir/web"; then
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_dist" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready" "$service_enabled_by_install"
    die "failed to stage legacy frontend directory for cleanup"
  fi
  if ! install -m 0755 "$binary_temp" "$PANEL_BIN"; then
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_dist" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready" "$service_enabled_by_install"
    die "failed to install $PANEL_BIN"
  fi
  if ! prepare_binary_permissions; then
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_dist" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready" "$service_enabled_by_install"
    die "failed to apply secure file permissions"
  fi
  if ! write_service_unit; then
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_dist" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready" "$service_enabled_by_install"
    die "failed to install the systemd service unit"
  fi
  if systemctl enable "$SERVICE_NAME" >/dev/null 2>&1; then
    service_enabled_by_install=1
  fi

  info "Starting $SERVICE_NAME"
  if ! systemctl restart "$SERVICE_NAME"; then
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_dist" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready" "$service_enabled_by_install"
    die "failed to start $SERVICE_NAME"
  fi

  if ! wait_for_health; then
    printf 'Error: the panel did not become healthy in 60 seconds. Recent logs follow:\n' >&2
    journalctl -u "$SERVICE_NAME" -n 100 --no-pager >&2 2>/dev/null || true
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_dist" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready" "$service_enabled_by_install"
    die "installation failed; see the logs above"
  fi

  if ! clear_binary_bootstrap_password || ! systemctl restart "$SERVICE_NAME" || ! wait_for_health; then
    restore_previous_install_or_die "$rollback_dir" "$had_previous_binary" "$had_previous_dist" "$had_previous_web" "$had_previous_unit" "$had_previous_config" "$data_snapshot_ready" "$service_enabled_by_install"
    die "failed to remove the one-time administrator password; the previous installation was restored"
  fi
  BOOTSTRAP_PASSWORD_WRITTEN=0
  rm -rf -- "$rollback_dir"
  info "SingBox Panel is healthy"
  print_binary_summary
}

restore_previous_install() {
  local rollback_dir="$1"
  local had_binary="$2"
  local had_dist="$3"
  local had_web="$4"
  local had_unit="$5"
  local had_config="$6"
  local restore_data="$7"
  local enabled_by_install="${8:-0}"
  local failed=0
  printf 'Restoring the previous panel release\n' >&2
  systemctl stop "$SERVICE_NAME" >/dev/null 2>&1 || true
  if [[ -d "$rollback_dir/dist" ]]; then
    if ! rm -rf -- "$INSTALL_REAL/dist" ||
       ! mv -- "$rollback_dir/dist" "$INSTALL_REAL/dist"; then
      failed=1
    fi
  elif [[ "$had_dist" -eq 1 ]]; then
    failed=1
  fi
  if [[ -d "$rollback_dir/web" ]]; then
    if ! rm -rf -- "$INSTALL_REAL/web" ||
       ! mv -- "$rollback_dir/web" "$INSTALL_REAL/web"; then
      failed=1
    fi
  elif [[ "$had_web" -eq 1 ]]; then
    failed=1
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
  if [[ "$had_unit" -eq 0 && "$enabled_by_install" -eq 1 ]]; then
    systemctl disable "$SERVICE_NAME" >/dev/null 2>&1 || failed=1
  fi
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
