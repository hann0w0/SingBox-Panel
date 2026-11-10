import { useEffect, useState } from 'react'
import { Button, Dropdown } from '../../../ui'
import { ChevronDown, Eye, EyeOff, RotateCw } from 'lucide-react'
import type { Server } from '../../../types'
import { maskHost } from '../../../util'
import { PageHeader, Status } from '../../../ui/Page'

/** Server detail heading: name, one meta line and the server-level actions. */
export function ServerOverviewCard({
  server, publicURL, busy, refreshing,
  onRefresh, onEditServer, onInstallSingbox, onUninstallSingbox, onInstallAgent, onUninstallAgent,
  onImport, onEditConfig, onRestart,
}: {
  server: Server
  publicURL: string
  busy: string
  refreshing: boolean
  onRefresh: () => void
  onEditServer?: () => void
  onInstallSingbox: () => void
  onUninstallSingbox: () => void
  onInstallAgent: () => void
  onUninstallAgent: () => void
  onImport: () => void
  onEditConfig: () => void
  onRestart: () => void
}) {
  const [showAddress, setShowAddress] = useState(false)
  useEffect(() => {
    setShowAddress(false)
  }, [server.id])

  const address = server.address || server.public_ip
  const extraIP = server.public_ip && server.public_ip !== server.address ? server.public_ip : ''
  const displayedAddress = showAddress ? address : maskHost(address)
  const displayedExtraIP = extraIP ? (showAddress ? extraIP : maskHost(extraIP)) : ''

  return <PageHeader
    title={server.name}
    meta={<>
      <Status tone={server.online ? 'ok' : 'off'}>{server.online ? '在线' : '离线'}</Status>
      {address && (
        <span className="sd-addr mono" title={showAddress ? (server.agent_url || publicURL || undefined) : undefined}>
          <span className="sd-addr-text" onClick={() => setShowAddress((prev) => !prev)}>
            {displayedAddress}{displayedExtraIP ? ` · ${displayedExtraIP}` : ''}
          </span>
          <button
            type="button"
            className="sd-addr-toggle"
            aria-label={showAddress ? '隐藏连接地址与 IP' : '显示连接地址与 IP'}
            title={showAddress ? '隐藏连接地址与 IP' : '显示连接地址与 IP'}
            onClick={() => setShowAddress((prev) => !prev)}
          >
            {showAddress ? <EyeOff size={13} /> : <Eye size={13} />}
          </button>
        </span>
      )}
      <span>
        {server.singbox_installed ? `sing-box ${server.singbox_version || ''}` : '未安装 sing-box'}
        {server.singbox_has_update && <span className="sd-up" title={`可升级至 ${server.singbox_latest_version || ''}`}> ↑</span>}
      </span>
      <span>Agent {server.agent_version || '—'}{server.agent_has_update && <span className="sd-up"> ↑</span>}</span>
      {server.os && <span>{server.os}</span>}
      {server.remark?.trim() && <span className="sd-remark">{server.remark}</span>}
    </>}
    actions={<>
      <Button icon={<RotateCw />} loading={refreshing} onClick={onRefresh} aria-label="刷新状态" title="刷新状态" />
      <Button onClick={onEditConfig}>配置文件</Button>
      <Dropdown placement="bottomRight" menu={{ items: [
        ...(onEditServer ? [
          { key: 'edit', label: '编辑节点', disabled: !!busy, onClick: onEditServer },
          { type: 'divider' as const },
        ] : []),
        { key: 'restart', label: '重启 sing-box', disabled: !server.online || !!busy, onClick: onRestart },
        { key: 'import', label: '识别现有配置', disabled: !!busy, onClick: onImport },
        { type: 'divider' },
        { key: 'singbox', label: '安装 / 升级 sing-box', disabled: !!busy, onClick: onInstallSingbox },
        { key: 'agent', label: '安装 / 升级 Agent', disabled: !!busy, onClick: onInstallAgent },
        { type: 'divider' },
        { key: 'uninstall-singbox', label: '卸载 sing-box', danger: true, disabled: !server.online || !server.singbox_installed || !!busy, onClick: onUninstallSingbox },
        { key: 'uninstall-agent', label: '卸载 Agent', danger: true, disabled: !server.online || !!busy, onClick: onUninstallAgent },
      ] }}>
        <Button loading={!!busy}>维护 <ChevronDown style={{ fontSize: 10 }} /></Button>
      </Dropdown>
    </>}
  />
}
