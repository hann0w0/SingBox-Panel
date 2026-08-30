package agent

import "context"

// Keep the managed configuration and state directories so reinstalling
// sing-box can resume from the panel without destroying node data.
const uninstallSingboxScript = `set -eu
found=0
backup_root="$(mktemp -d /tmp/singbox-panel-singbox-uninstall.XXXXXX)"
cleanup() { rm -rf "$backup_root"; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
umask 077
if [ -d /etc/sing-box ]; then cp -a /etc/sing-box "$backup_root/config"; fi
if [ -d /var/lib/sing-box ]; then cp -a /var/lib/sing-box "$backup_root/state"; fi

if command -v systemctl >/dev/null 2>&1; then
  if systemctl list-unit-files sing-box.service >/dev/null 2>&1; then found=1; fi
  systemctl disable --now sing-box.service >/dev/null 2>&1 || true
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

if [ "$found" = "0" ]; then
  echo 'sing-box 未安装，无需卸载'
else
  echo 'sing-box 已卸载；配置目录 /etc/sing-box 和面板节点数据均已保留'
fi
`

func UninstallSingbox(ctx context.Context) (string, error) {
	return runShell(ctx, uninstallSingboxScript)
}
