import { Button, Dropdown } from '../../../ui'
import { ChevronDown, Download, FileJson } from 'lucide-react'
import type { Server } from '../../../types'

/** Compact server actions; identity lives in the breadcrumb and tabs. */
export function ServerOverviewCard({
  server, busy,
  onInstallSingbox, onUninstallSingbox, onInstallAgent, onUninstallAgent,
  onImport, onEditConfig, onRestart, onExport,
}: {
  server: Server
  busy: string
  onInstallSingbox: () => void
  onUninstallSingbox: () => void
  onInstallAgent: () => void
  onUninstallAgent: () => void
  onImport: () => void
  onEditConfig: () => void
  onRestart: () => void
  onExport: () => void
}) {
  return <header className="sd-overview">
    <div className="sd-overview-actions">
      <Button icon={<Download />} onClick={onExport}>导出节点</Button>
      <Button icon={<FileJson />} onClick={onEditConfig}>配置文件</Button>
      <Dropdown placement="bottomRight" menu={{ items: [
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
    </div>
  </header>
}
