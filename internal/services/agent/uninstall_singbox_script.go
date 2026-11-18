package agent

// Keep the managed configuration and state directories so reinstalling
// sing-box can resume from the panel without destroying node data.
const uninstallSingboxScript = `set -eu
found=0
service_manager=""
was_active=0
was_enabled=0
umask 077
# The backup parent must exist before mktemp runs: under set -e a missing
# directory would abort here, before the restore trap is even installed.
mkdir -p /var/lib/singbox-panel-agent
backup_root="$(mktemp -d /var/lib/singbox-panel-agent/.singbox-uninstall.XXXXXX)"
committed=0

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
    if [ -f "$backup_root/init" ]; then
      mkdir -p /etc/init.d
      cp -a "$backup_root/init" /etc/init.d/sing-box || true
    fi
    if [ -f "$backup_root/conf" ]; then
      mkdir -p /etc/conf.d
      cp -a "$backup_root/conf" /etc/conf.d/sing-box || true
    fi
    if [ "$service_manager" = systemd ]; then
      systemctl daemon-reload >/dev/null 2>&1 || true
      if [ "$was_enabled" = "1" ]; then systemctl enable sing-box.service >/dev/null 2>&1 || true; fi
      if [ "$was_active" = "1" ]; then systemctl restart sing-box.service >/dev/null 2>&1 || true; fi
    elif [ "$service_manager" = openrc ]; then
      if [ "$was_enabled" = "1" ]; then rc-update add sing-box default >/dev/null 2>&1 || true; fi
      if [ "$was_active" = "1" ]; then rc-service sing-box start >/dev/null 2>&1 || true; fi
    fi
    echo "恢复副本保留在 $backup_root" >&2
  else
    rm -rf "$backup_root"
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

if [ -d /etc/sing-box ]; then cp -a /etc/sing-box "$backup_root/config"; fi
if [ -d /var/lib/sing-box ]; then cp -a /var/lib/sing-box "$backup_root/state"; fi
if [ -f /etc/init.d/sing-box ]; then cp -a /etc/init.d/sing-box "$backup_root/init"; fi
if [ -f /etc/conf.d/sing-box ]; then cp -a /etc/conf.d/sing-box "$backup_root/conf"; fi

if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  service_manager=systemd
  if systemctl list-unit-files sing-box.service 2>/dev/null | grep -q 'sing-box\.service'; then found=1; fi
  if systemctl is-active --quiet sing-box.service; then was_active=1; fi
  if systemctl is-enabled --quiet sing-box.service; then was_enabled=1; fi
  systemctl disable --now sing-box.service >/dev/null 2>&1 || true
  if systemctl is-active --quiet sing-box.service; then
    echo 'sing-box 服务仍在运行，拒绝继续卸载' >&2
    exit 1
  fi
elif command -v rc-service >/dev/null 2>&1 && [ -x /etc/init.d/sing-box ]; then
  service_manager=openrc
  found=1
  if rc-service sing-box status >/dev/null 2>&1; then was_active=1; fi
  if [ -e /etc/runlevels/default/sing-box ]; then was_enabled=1; fi
  rc-service sing-box stop >/dev/null 2>&1 || true
  rc-update del sing-box default >/dev/null 2>&1 || true
  if rc-service sing-box status >/dev/null 2>&1; then
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
if [ -f /etc/init.d/sing-box ] && grep -F "Managed by SingBox Panel" /etc/init.d/sing-box >/dev/null 2>&1; then
  rm -f /etc/init.d/sing-box
fi
if [ -f /etc/conf.d/sing-box ] && grep -F "BEGIN Managed by SingBox Panel" /etc/conf.d/sing-box >/dev/null 2>&1; then
  rm -f /etc/conf.d/sing-box
fi
if [ -d "$backup_root/config" ]; then
  mkdir -p /etc/sing-box
  cp -a "$backup_root/config/." /etc/sing-box/
fi
if [ -d "$backup_root/state" ]; then
  mkdir -p /var/lib/sing-box
  cp -a "$backup_root/state/." /var/lib/sing-box/
fi
if [ "$service_manager" = systemd ]; then systemctl daemon-reload >/dev/null 2>&1 || true; fi
committed=1

if [ "$found" = "0" ]; then
  echo 'sing-box 未安装，无需卸载'
else
  echo 'sing-box 已卸载；配置目录 /etc/sing-box 和面板节点数据均已保留'
fi
`
