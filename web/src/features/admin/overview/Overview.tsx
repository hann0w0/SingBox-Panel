import { useEffect, useRef, useState } from 'react'
import { Card, Col, Progress, Row, Statistic, Table, Tag } from 'antd'
import {
  ApiOutlined,
  BranchesOutlined,
  CloudServerOutlined,
  TeamOutlined,
} from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { errMsg, getOverview, isCanceledRequest } from '../../../api'
import type { NodeBrief, Overview as OverviewData } from '../../../types'
import { formatBytes, formatDuration } from '../../../util'
import { RequestState } from '../../../components/RequestState'

function usageColor(pct: number): string {
  if (pct >= 85) return '#ff4d4f'
  if (pct >= 60) return '#faad14'
  return '#52c41a'
}

export default function Overview() {
  const nav = useNavigate()
  const [d, setD] = useState<OverviewData | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [reloadKey, setReloadKey] = useState(0)
  const dataRef = useRef<OverviewData | null>(null)

  useEffect(() => {
    let active = true
    let timer: number | undefined
    let controller: AbortController | null = null

    const load = async () => {
      controller = new AbortController()
      if (!dataRef.current) setLoading(true)
      try {
        const next = await getOverview(controller.signal)
        if (active) {
          dataRef.current = next
          setD(next)
          setError(null)
        }
      } catch (reason) {
        if (active && !isCanceledRequest(reason)) setError(errMsg(reason))
      } finally {
        controller = null
        if (active) setLoading(false)
        if (active) timer = window.setTimeout(load, 3000)
      }
    }

    void load()
    return () => {
      active = false
      if (timer !== undefined) window.clearTimeout(timer)
      controller?.abort()
    }
  }, [reloadKey])

  const cpu = Math.round(d?.cpu_percent ?? 0)
  const mem = Math.round(d?.mem_percent ?? 0)
  const nodes = d?.nodes ?? []

  const gauge = (title: string, pct: number, footer: React.ReactNode) => (
    <Card>
      <div style={{ color: 'rgba(0,0,0,0.45)', marginBottom: 8 }}>{title}</div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 20 }}>
        <Progress type="dashboard" percent={pct} size={120} strokeColor={usageColor(pct)} />
        <div style={{ color: 'rgba(0,0,0,0.65)' }}>{footer}</div>
      </div>
    </Card>
  )

  const stat = (title: string, value: React.ReactNode, icon: React.ReactNode, extra?: React.ReactNode) => (
    <Card style={{ height: '100%' }}>
      <Statistic title={title} value={value as any} prefix={icon} />
      <div style={{ marginTop: 6, fontSize: 12, color: 'rgba(0,0,0,0.45)', minHeight: 18, lineHeight: '18px' }}>
        {extra || '\u00A0'}
      </div>
    </Card>
  )

  return (
    <RequestState loading={loading} error={error} hasData={!!d} onRetry={() => setReloadKey((value) => value + 1)}>
      <Row gutter={[16, 16]}>
      <Col xs={24} md={12}>
        {gauge('面板服务器 · CPU 占用', cpu, <span>实时占用 {cpu}%</span>)}
      </Col>
      <Col xs={24} md={12}>
        {gauge(
          '面板服务器 · 内存占用',
          mem,
          <div>
            <div>{formatBytes(d?.mem_used ?? 0)}</div>
            <div style={{ color: 'rgba(0,0,0,0.45)' }}>/ {formatBytes(d?.mem_total ?? 0)}</div>
          </div>,
        )}
      </Col>

      <Col xs={12} sm={12} md={6}>
        {stat(
          '节点',
          d ? `${d.servers_online} / ${d.servers_total}` : '—',
          <CloudServerOutlined />,
          d && d.servers_online < d.servers_total ? (
            <span style={{ color: '#ff4d4f' }}>{d.servers_total - d.servers_online} 台离线</span>
          ) : (
            '全部在线'
          ),
        )}
      </Col>
      <Col xs={12} sm={12} md={6}>
        {stat(
          '用户',
          d?.users_total ?? '—',
          <TeamOutlined />,
          d ? `启用 ${d.users_enabled}${d.users_expiring > 0 ? ` · 7 天到期 ${d.users_expiring}` : ''}` : '启用 —',
        )}
      </Col>
      <Col xs={12} sm={12} md={6}>
        {stat('入站', d?.inbounds_total ?? 0, <ApiOutlined />, `出站 ${d?.outbounds_total ?? 0}`)}
      </Col>
      <Col xs={12} sm={12} md={6}>
        {stat('规则', d?.rules_total ?? 0, <BranchesOutlined />, `已运行 ${formatDuration(d?.uptime_seconds ?? 0)}`)}
      </Col>

      <Col xs={24}>
        <Card
          title="节点状态"
          extra={<span style={{ fontSize: 12, color: 'rgba(0,0,0,0.45)' }}>点击一行进入该节点</span>}
        >
          <Table
            rowKey="id"
            size="small"
            pagination={false}
            dataSource={nodes}
            locale={{ emptyText: '还没有节点' }}
            onRow={(n) => ({
              onClick: () => nav(`/admin/servers/${n.id}`),
              onKeyDown: (event) => {
                if (event.key === 'Enter' || event.key === ' ') {
                  event.preventDefault()
                  nav(`/admin/servers/${n.id}`)
                }
              },
              role: 'link',
              tabIndex: 0,
              style: { cursor: 'pointer' },
            })}
            columns={[
              {
                title: '节点',
                render: (_, n: NodeBrief) => <span>{n.name}</span>,
              },
              {
                title: '状态',
                render: (_, n: NodeBrief) => (n.online ? <Tag color="green">在线</Tag> : <Tag>离线</Tag>),
              },
              {
                title: 'sing-box',
                responsive: ['sm'],
                render: (_, n: NodeBrief) =>
                  n.singbox_installed ? <Tag color="blue">{n.singbox_version || '已安装'}</Tag> : <Tag color="orange">未安装</Tag>,
              },
              {
                title: '负载',
                responsive: ['md'],
                render: (_, n: NodeBrief) => (n.online ? n.load1.toFixed(2) : '—'),
              },
              {
                title: '内存',
                responsive: ['md'],
                render: (_, n: NodeBrief) => {
                  if (!n.online || !n.mem_total) return '—'
                  const pct = Math.round((n.mem_used / n.mem_total) * 100)
                  return (
                    <span style={{ color: usageColor(pct) }}>
                      {pct}% · {formatBytes(n.mem_used)}
                    </span>
                  )
                },
              },
              {
                title: '配置',
                responsive: ['sm'],
                render: (_, n: NodeBrief) => (
                  <span style={{ color: 'rgba(0,0,0,0.65)' }}>
                    入 {n.inbounds} · 出 {n.outbounds} · 规则 {n.rules}
                  </span>
                ),
              },
            ]}
          />
        </Card>
      </Col>
      </Row>
    </RequestState>
  )
}
