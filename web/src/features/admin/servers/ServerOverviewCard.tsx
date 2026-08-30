import { Button, Card, Descriptions, Space, Tag, Typography } from 'antd'
import { ArrowUpOutlined, DeleteOutlined, ReloadOutlined } from '@ant-design/icons'
import type { Server } from '../../../types'
import { formatBytes } from '../../../util'

export function ServerOverviewCard({
  server, publicURL, busy, refreshing, exportLoading,
  onRefresh, onInstallSingbox, onUninstallSingbox, onInstallAgent, onUninstallAgent,
  onImport, onEditConfig, onRestart, onExport,
}: {
  server: Server
  publicURL: string
  busy: string
  refreshing: boolean
  exportLoading: boolean
  onRefresh: () => void
  onInstallSingbox: () => void
  onUninstallSingbox: () => void
  onInstallAgent: () => void
  onUninstallAgent: () => void
  onImport: () => void
  onEditConfig: () => void
  onRestart: () => void
  onExport: () => void
}) {
  return (
    <Card
      title={(
        <Space wrap style={{ rowGap: 4 }}>
          {server.name}
          {server.online ? <Tag color="green">在线</Tag> : <Tag>离线</Tag>}
          {server.singbox_installed ? (
            server.singbox_has_update ? (
              <Tag
                color="orange"
                icon={<ArrowUpOutlined />}
                title={`发现新版本 (${server.singbox_latest_version})`}
              >
                {server.singbox_version} (可升级)
              </Tag>
            ) : (
              <Tag color="blue">{server.singbox_version}</Tag>
            )
          ) : (
            <Tag color="orange">未装 sing-box</Tag>
          )}
        </Space>
      )}
      extra={<Button icon={<ReloadOutlined />} loading={refreshing} onClick={onRefresh} title="刷新节点状态" aria-label="刷新节点状态" />}
    >
      <Descriptions column={{ xs: 1, sm: 2, lg: 3 }} size="small" bordered>
        <Descriptions.Item label="节点地址">
          <Space size={6} wrap>
            <span>{server.address || '—'}</span>
            {server.public_ip && server.public_ip !== server.address && <Typography.Text type="secondary">({server.public_ip})</Typography.Text>}
          </Space>
        </Descriptions.Item>
		<Descriptions.Item label="通信地址">{server.agent_url || publicURL || '旧版 Agent 未上报'}</Descriptions.Item>
		<Descriptions.Item label="Agent">{server.agent_version || '旧版未上报'}</Descriptions.Item>
		<Descriptions.Item label="系统">{server.os || '—'}</Descriptions.Item>
        <Descriptions.Item label="负载">{server.load1?.toFixed(2) ?? '—'}</Descriptions.Item>
        <Descriptions.Item label="内存">{formatBytes(server.mem_used)}/{formatBytes(server.mem_total)}</Descriptions.Item>
      </Descriptions>

      <Space wrap style={{ marginTop: 16 }}>
        <Button type="primary" loading={busy === 'install'} onClick={onInstallSingbox}>安装 / 升级 Sing-box</Button>
        <Button danger icon={<DeleteOutlined />} loading={busy === 'uninstall-singbox'} disabled={!server.online || !server.singbox_installed} onClick={onUninstallSingbox}>卸载 Sing-box</Button>
        <Button type="primary" loading={busy === 'agent'} onClick={onInstallAgent}>安装 / 升级 Agent</Button>
        <Button danger icon={<DeleteOutlined />} disabled={!server.online} onClick={onUninstallAgent}>卸载 Agent</Button>
        <Button loading={busy === 'import'} onClick={onImport}>识别配置</Button>
        <Button onClick={onEditConfig}>编辑配置</Button>
        <Button loading={busy === 'restart'} onClick={onRestart}>重启服务</Button>
        <Button loading={exportLoading} onClick={onExport}>导出节点</Button>
      </Space>
    </Card>
  )
}
