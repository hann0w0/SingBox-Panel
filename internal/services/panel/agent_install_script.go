package panel

// agentInstallScript is served at /api/agent/install.sh. It installs the
// SingBox Panel Agent under systemd or Alpine's OpenRC; the Agent then installs
// and manages the official sing-box on the host.
const agentInstallScript = `#!/bin/sh
set -eu

# The panel hands out one short command:
#   curl -fsSL <panel>/api/agent/install.sh | sh -s -- --url <panel> --code <code>
# When that runs as an ordinary account, re-download this same script and feed
# it to root through sudo or doas. Re-fetching instead of re-reading stdin is
# deliberate: the invoking shell may already have buffered the rest of a piped
# script, so a child process could otherwise see a truncated installer. Keeping
# the escalation here is what allows the pasted command to stay on one line.
if [ "$(id -u)" != "0" ]; then
  panel_url=""; previous=""
  for arg in "$@"; do
    [ "$previous" = "--url" ] && panel_url="$arg"
    previous="$arg"
  done
  case "$panel_url" in
    https://*) ;;
    *) echo "--url is required to run the installer as root" >&2; exit 1 ;;
  esac
  for elevate in sudo doas; do
    command -v "$elevate" >/dev/null 2>&1 || continue
    exec "$elevate" sh -c 'if command -v curl >/dev/null 2>&1; then curl -fsSL "$0/api/agent/install.sh"; else wget -qO- "$0/api/agent/install.sh"; fi | sh -s -- "$@"' "$panel_url" "$@"
  done
  echo "run this command as root or install sudo/doas" >&2
  exit 1
fi

URL=""; TOKEN=""; CODE=""; TMP=""; AUTH_CONFIG=""
BIN=/usr/local/bin/singbox-panel-agent
PREV=/usr/local/bin/singbox-panel-agent.prev
CONFIG=/etc/singbox-panel-agent/agent.conf
SYSTEMD_UNIT=/etc/systemd/system/singbox-panel-agent.service
OPENRC_INIT=/etc/init.d/singbox-panel-agent
SERVICE_FILE=""; SERVICE_MANAGER=""
BACKUP_DIR=""; CONFIG_TMP=""; UNIT_TMP=""; RESTORE_TMP=""
READY=/run/singbox-panel-agent.ready
EXPECTED_READY=/run/singbox-panel-agent.expected-sha256
TRANSACTION_STARTED=0; COMMITTED=0
HAD_BIN=0; HAD_CONFIG=0; HAD_UNIT=0; WAS_ACTIVE=0; WAS_ENABLED=0

curl_agent() {
  curl --fail --silent --show-error --location --connect-timeout 10 --max-time 300 \
    --retry 3 --retry-delay 2 --retry-connrefused "$@"
}

service_prepare() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl daemon-reload
  fi
}

service_stop() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl stop singbox-panel-agent >/dev/null 2>&1 || true
  else
    rc-service singbox-panel-agent stop >/dev/null 2>&1 || true
  fi
}

service_restart() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl restart singbox-panel-agent
  else
    rc-service singbox-panel-agent stop >/dev/null 2>&1 || true
    rc-service singbox-panel-agent start
  fi
}

service_active() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl is-active --quiet singbox-panel-agent
  else
    rc-service singbox-panel-agent status >/dev/null 2>&1
  fi
}

service_enabled() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl is-enabled --quiet singbox-panel-agent
  else
    [ -e /etc/runlevels/default/singbox-panel-agent ]
  fi
}

service_enable() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl enable singbox-panel-agent >/dev/null 2>&1
  else
    rc-update add singbox-panel-agent default >/dev/null 2>&1
  fi
}

service_disable() {
  if [ "$SERVICE_MANAGER" = systemd ]; then
    systemctl disable singbox-panel-agent >/dev/null 2>&1 || true
  else
    rc-update del singbox-panel-agent default >/dev/null 2>&1 || true
  fi
}

restore_target() {
  target="$1"; backup="$2"; existed="$3"
  if [ "$existed" = "1" ]; then
    RESTORE_TMP="$(mktemp "$(dirname "$target")/.singbox-panel-restore.XXXXXX")" || return 1
    cp -p "$backup" "$RESTORE_TMP" || return 1
    mv -f "$RESTORE_TMP" "$target" || return 1
    RESTORE_TMP=""
  else
    rm -f "$target" || return 1
  fi
}

cleanup() {
  status=$?
  trap - EXIT HUP INT TERM
  set +e
  if [ "$TRANSACTION_STARTED" = "1" ] && [ "$COMMITTED" != "1" ]; then
    echo "Agent installation failed; restoring the previous binary, configuration and service" >&2
    service_stop
    rollback_failed=0
    restore_target "$BIN" "$BACKUP_DIR/bin" "$HAD_BIN" || rollback_failed=1
    restore_target "$CONFIG" "$BACKUP_DIR/config" "$HAD_CONFIG" || rollback_failed=1
    restore_target "$SERVICE_FILE" "$BACKUP_DIR/unit" "$HAD_UNIT" || rollback_failed=1
    service_prepare >/dev/null 2>&1 || rollback_failed=1
    if [ "$WAS_ENABLED" = "1" ]; then
      service_enable || rollback_failed=1
    else
      service_disable
    fi
    if [ "$WAS_ACTIVE" = "1" ]; then
      service_restart >/dev/null 2>&1 || rollback_failed=1
    fi
    if [ "$rollback_failed" = "1" ]; then
      echo "automatic Agent rollback was incomplete; inspect $BACKUP_DIR" >&2
      BACKUP_DIR=""
    fi
  fi
  [ -z "$TMP" ] || rm -f "$TMP"
  [ -z "$CONFIG_TMP" ] || rm -f "$CONFIG_TMP"
  [ -z "$UNIT_TMP" ] || rm -f "$UNIT_TMP"
  [ -z "$RESTORE_TMP" ] || rm -f "$RESTORE_TMP"
  [ -z "$AUTH_CONFIG" ] || rm -f "$AUTH_CONFIG"
  [ "$COMMITTED" = "1" ] || rm -f "$EXPECTED_READY"
  [ -z "$BACKUP_DIR" ] || rm -rf "$BACKUP_DIR"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

while [ $# -gt 0 ]; do
  case "$1" in
    --url|--token|--code)
      [ $# -ge 2 ] && [ -n "$2" ] || { echo "$1 requires a value"; exit 1; }
      case "$1" in
        --url) URL="$2" ;;
        --token) TOKEN="$2" ;;
        --code) CODE="$2" ;;
      esac
      shift 2
      ;;
    --insecure) echo "--insecure is disabled in production"; exit 1 ;;
    *) echo "unknown option: $1"; exit 1 ;;
  esac
done
[ "$(id -u)" = "0" ] || { echo "must run as root"; exit 1; }
case "$URL" in
  https://*) ;;
  *) echo "--url must be a non-empty HTTPS URL"; exit 1 ;;
esac
case "$URL" in
  *[[:space:]]*) echo "--url must not contain whitespace"; exit 1 ;;
esac

if ! command -v curl >/dev/null 2>&1; then
  if command -v apk >/dev/null 2>&1; then
    apk add --no-cache curl ca-certificates
  elif command -v apt-get >/dev/null 2>&1; then
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y curl ca-certificates
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y curl ca-certificates
  elif command -v yum >/dev/null 2>&1; then
    yum install -y curl ca-certificates
  else
    echo "curl is required and no supported package manager was found"; exit 1
  fi
fi

if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  SERVICE_MANAGER=systemd
  SERVICE_FILE=$SYSTEMD_UNIT
elif command -v rc-service >/dev/null 2>&1 && command -v rc-update >/dev/null 2>&1 && [ -d /etc/init.d ]; then
  SERVICE_MANAGER=openrc
  SERVICE_FILE=$OPENRC_INIT
else
  echo "systemd or OpenRC is required"; exit 1
fi

if [ -z "$TOKEN" ]; then
  [ -n "$CODE" ] || { echo "missing one-time registration code"; exit 1; }
  TOKEN="$(curl_agent -X POST --data-urlencode "code=$CODE" "$URL/api/agent/register")" || {
    echo "registration code exchange failed"; exit 1;
  }
fi
case "$TOKEN" in
  *[!0-9a-fA-F]*|'') echo "panel returned an invalid Agent token"; exit 1 ;;
esac
[ "${#TOKEN}" -ge 32 ] && [ "${#TOKEN}" -le 128 ] || {
  echo "panel returned an invalid Agent token length"; exit 1;
}

# Keep the long-lived token out of curl's argv (and therefore /proc/*/cmdline).
AUTH_CONFIG="$(mktemp /tmp/.singbox-panel-agent-auth.XXXXXX)"
chmod 600 "$AUTH_CONFIG"
printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" > "$AUTH_CONFIG"

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) echo "unsupported arch: $ARCH"; exit 1 ;;
esac

echo "downloading singbox-panel-agent ($GOARCH) ..."
TMP="$(mktemp /usr/local/bin/.singbox-panel-agent.XXXXXX)"
curl_agent --config "$AUTH_CONFIG" "$URL/api/agent/download?arch=$GOARCH" -o "$TMP"
EXPECTED="$(curl_agent --config "$AUTH_CONFIG" "$URL/api/agent/checksum?arch=$GOARCH" | tr -d '[:space:]')"
case "$EXPECTED" in
  *[!0-9a-fA-F]*|'') echo "invalid Agent checksum"; exit 1 ;;
esac
[ "${#EXPECTED}" = "64" ] || { echo "invalid Agent checksum length"; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL="$(sha256sum "$TMP" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL="$(shasum -a 256 "$TMP" | awk '{print $1}')"
elif command -v openssl >/dev/null 2>&1; then
  ACTUAL="$(openssl dgst -sha256 "$TMP" | awk '{print $NF}')"
else
  echo "sha256sum, shasum or openssl is required"; exit 1
fi
[ "$(printf '%s' "$ACTUAL" | tr 'A-F' 'a-f')" = "$(printf '%s' "$EXPECTED" | tr 'A-F' 'a-f')" ] || {
  echo "Agent SHA256 mismatch"; exit 1;
}
chmod 755 "$TMP"
"$TMP" --version | grep -q '^singbox-panel-agent ' || { echo "invalid Agent binary"; exit 1; }

mkdir -p /etc/singbox-panel-agent /var/lib/singbox-panel-agent "$(dirname "$SERVICE_FILE")"
BACKUP_DIR="$(mktemp -d /var/lib/singbox-panel-agent/.install-rollback.XXXXXX)"
chmod 700 "$BACKUP_DIR"
if [ -f "$BIN" ]; then cp -p "$BIN" "$BACKUP_DIR/bin"; HAD_BIN=1; fi
if [ -f "$CONFIG" ]; then cp -p "$CONFIG" "$BACKUP_DIR/config"; HAD_CONFIG=1; fi
if [ -f "$SERVICE_FILE" ]; then cp -p "$SERVICE_FILE" "$BACKUP_DIR/unit"; HAD_UNIT=1; fi
if service_active; then WAS_ACTIVE=1; fi
if service_enabled; then WAS_ENABLED=1; fi
TRANSACTION_STARTED=1

printf '%s\n' "$ACTUAL" > "$EXPECTED_READY"
chmod 644 "$EXPECTED_READY"
mv -f "$TMP" "$BIN"
TMP=""

CONFIG_TMP="$(mktemp /etc/singbox-panel-agent/.agent.conf.XXXXXX)"
umask 077
printf 'URL=%s\nTOKEN=%s\n' "$URL" "$TOKEN" > "$CONFIG_TMP"
chmod 600 "$CONFIG_TMP"
mv -f "$CONFIG_TMP" "$CONFIG"
CONFIG_TMP=""

if [ "$SERVICE_MANAGER" = systemd ]; then
  UNIT_TMP="$(mktemp /etc/systemd/system/.singbox-panel-agent.service.XXXXXX)"
  cat > "$UNIT_TMP" <<EOF
[Unit]
Description=SingBox Panel Agent
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
ExecStart=/usr/local/bin/singbox-panel-agent --config /etc/singbox-panel-agent/agent.conf
Restart=always
RestartSec=5s
LimitNOFILE=infinity
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true

[Install]
WantedBy=multi-user.target
EOF
  chmod 644 "$UNIT_TMP"
else
  UNIT_TMP="$(mktemp /etc/init.d/.singbox-panel-agent.XXXXXX)"
  cat > "$UNIT_TMP" <<EOF
#!/sbin/openrc-run
description="SingBox Panel Agent"
supervisor="supervise-daemon"
command="/usr/local/bin/singbox-panel-agent"
command_args="--config /etc/singbox-panel-agent/agent.conf"
command_user="root"
pidfile="/run/singbox-panel-agent.pid"
respawn_delay=5
respawn_max=0
output_log="/var/log/singbox-panel-agent.log"
error_log="/var/log/singbox-panel-agent.log"
umask 0077

depend() {
  after net
}
EOF
  chmod 755 "$UNIT_TMP"
fi
mv -f "$UNIT_TMP" "$SERVICE_FILE"
UNIT_TMP=""

service_prepare
service_enable
rm -f "$READY"
service_restart
i=0
while [ "$i" -lt 30 ]; do
  READY_SHA="$(sed -n '2p' "$READY" 2>/dev/null || true)"
  if service_active && [ "$READY_SHA" = "$ACTUAL" ]; then
    COMMITTED=1
    rm -f "$PREV" "$EXPECTED_READY"
    rm -rf "$BACKUP_DIR"
    BACKUP_DIR=""
    echo "singbox-panel-agent installed and connected."
    exit 0
  fi
  i=$((i + 1))
  sleep 2
done
echo "new Agent failed to connect" >&2
exit 1
`
