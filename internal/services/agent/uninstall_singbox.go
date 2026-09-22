package agent

import "context"

// Keep the managed configuration and state directories so reinstalling
// sing-box can resume from the panel without destroying node data.
const uninstallSingboxScript = `set -eu
found=0
backup_root="$(mktemp -d /var/lib/singbox-panel-agent/.singbox-uninstall.XXXXXX)"
committed=0
was_active=0
was_enabled=0
cleanup() {
  status=$?
  trap - EXIT HUP INT TERM
  set +e
  if [ "$committed" != "1" ]; then
    echo 'sing-box 卸载失败；正在恢复配置和服务状态' >&2
    if [ -d "$backup_root/config" ]; then
      mkdir -p /etc/sing-box
      cp -a "$backup_root/config/." /etc/sing-box/ || true
    fi
    if [ -d "$backup_root/state" ]; then
      mkdir -p /var/lib/sing-box
      cp -a "$backup_root/state/." /var/lib/sing-box/ || true
    fi
    if command -v systemctl >/dev/null 2>&1; then
      systemctl daemon-reload >/dev/null 2>&1 || true
      if [ "$was_enabled" = "1" ]; then systemctl enable sing-box.service >/dev/null 2>&1 || true; fi
      if [ "$was_active" = "1" ]; then systemctl restart sing-box.service >/dev/null 2>&1 || true; fi
    fi
    echo "恢复副本保留在 $backup_root" >&2
  else
    rm -rf "$backup_root"
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
umask 077
mkdir -p /var/lib/singbox-panel-agent
if [ -d /etc/sing-box ]; then cp -a /etc/sing-box "$backup_root/config"; fi
if [ -d /var/lib/sing-box ]; then cp -a /var/lib/sing-box "$backup_root/state"; fi

if command -v systemctl >/dev/null 2>&1; then
  if systemctl list-unit-files sing-box.service >/dev/null 2>&1; then found=1; fi
  if systemctl is-active --quiet sing-box.service; then was_active=1; fi
  if systemctl is-enabled --quiet sing-box.service; then was_enabled=1; fi
  systemctl disable --now sing-box.service >/dev/null 2>&1 || true
  if systemctl is-active --quiet sing-box.service; then
    echo 'sing-box 服务仍在运行，拒绝继续卸载' >&2
    exit 1
  fi
fi

if command -v dpkg-query >/dev/null 2>&1; then
  for pkg in sing-box-beta sing-box; do
    if dpkg-query -W -f='${Status}' "$pkg" 2>/dev/null | grep -q 'install ok installed'; then
      found=1
      if command -v apt-get >/dev/null 2>&1; then
        DEBIAN_FRONTEND=noninteractive apt-get remove -y "$pkg"
      else
        dpkg -r "$pkg"
      fi
    fi
  done
elif command -v rpm >/dev/null 2>&1; then
  for pkg in sing-box-beta sing-box; do
    if rpm -q "$pkg" >/dev/null 2>&1; then
      found=1
      if command -v dnf >/dev/null 2>&1; then dnf remove -y "$pkg"; else rpm -e "$pkg"; fi
    fi
  done
elif command -v pacman >/dev/null 2>&1; then
  for pkg in sing-box-beta sing-box; do
    if pacman -Q "$pkg" >/dev/null 2>&1; then found=1; pacman -R --noconfirm "$pkg"; fi
  done
elif command -v apk >/dev/null 2>&1; then
  for pkg in sing-box-beta sing-box; do
    if apk info -e "$pkg" >/dev/null 2>&1; then found=1; apk del "$pkg"; fi
  done
elif command -v opkg >/dev/null 2>&1; then
  for pkg in sing-box-beta sing-box; do
    if opkg status "$pkg" 2>/dev/null | grep -q '^Status:.* installed'; then found=1; opkg remove "$pkg"; fi
  done
fi

for path in /usr/bin/sing-box /usr/local/bin/sing-box /usr/sbin/sing-box /usr/local/sbin/sing-box /opt/sing-box/sing-box; do
  if [ -e "$path" ]; then found=1; rm -f "$path"; fi
done
rm -f /etc/systemd/system/sing-box.service /etc/systemd/system/sing-box@.service
rm -f /usr/lib/systemd/system/sing-box.service /usr/lib/systemd/system/sing-box@.service
rm -f /lib/systemd/system/sing-box.service /lib/systemd/system/sing-box@.service
if [ -d "$backup_root/config" ]; then
  mkdir -p /etc/sing-box
  cp -a "$backup_root/config/." /etc/sing-box/
fi
if [ -d "$backup_root/state" ]; then
  mkdir -p /var/lib/sing-box
  cp -a "$backup_root/state/." /var/lib/sing-box/
fi
if command -v systemctl >/dev/null 2>&1; then systemctl daemon-reload >/dev/null 2>&1 || true; fi
committed=1

if [ "$found" = "0" ]; then
  echo 'sing-box 未安装，无需卸载'
else
  echo 'sing-box 已卸载；配置目录 /etc/sing-box 和面板节点数据均已保留'
fi
`

func UninstallSingbox(ctx context.Context) (string, error) {
	return runShell(ctx, uninstallSingboxScript)
}
