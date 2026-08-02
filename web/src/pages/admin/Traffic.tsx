import { useCallback, useEffect, useRef, useState } from 'react'
import { Button, Card, Empty, Grid, Select, Segmented, Space, Table, Tag, Typography, message } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { errMsg, getServerTraffic, listServers } from '../../api'
import type { Server, TrafficPoint, TrafficRange, TrafficSeries } from '../../types'
import { formatBytes } from '../../util'

const RANGE_OPTIONS: { label: string; value: TrafficRange }[] = [
  { label: '实时', value: '15m' },
  { label: '30 分钟', value: '30m' },
  { label: '1 小时', value: '1h' },
  { label: '12 小时', value: '12h' },
  { label: '24 小时', value: '24h' },
  { label: '7 天', value: '7d' },
  { label: '30 天', value: '30d' },
]

function rate(n: number): string {
  return `${formatBytes(n)}/s`
}

function pointLabel(time: string, range: TrafficRange): string {
  const date = new Date(time)
  if (Number.isNaN(date.getTime())) return ''
  if (range === '15m' || range === '30m' || range === '1h' || range === '12h') {
    return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
  }
  return date.toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' })
}

function axisBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  if (n < 1024) return `${Math.round(n)} B`
  return formatBytes(n)
}

type TrendMode = 'network' | 'connections'

function TrafficTrend({ points, range, mode, stepSeconds }: { points: TrafficPoint[]; range: TrafficRange; mode: TrendMode; stepSeconds: number }) {
  const width = 760
  const height = 250
  const left = 52
  const right = 16
  const top = 18
  const bottom = 34
  const chartWidth = width - left - right
  const chartHeight = height - top - bottom
  const valueFor = (point: TrafficPoint, key: 'upload' | 'download' | 'tcp_connections' | 'udp_connections') => {
    if (mode === 'network' && (key === 'upload' || key === 'download')) {
      const sampledRate = key === 'upload' ? point.upload_rate : point.download_rate
      return sampledRate > 0 ? sampledRate : point[key] / Math.max(1, stepSeconds)
    }
    return point[key]
  }
  const max = mode === 'network'
    ? Math.max(1, ...points.flatMap((p) => [valueFor(p, 'upload'), valueFor(p, 'download')]))
    : Math.max(1, ...points.flatMap((p) => [p.tcp_connections, p.udp_connections]))
  const x = (index: number) => left + (points.length <= 1 ? chartWidth / 2 : (index / (points.length - 1)) * chartWidth)
  const y = (value: number) => top + chartHeight - (value / max) * chartHeight
  const line = (key: 'upload' | 'download' | 'tcp_connections' | 'udp_connections') =>
    points.map((point, index) => `${x(index)},${y(valueFor(point, key))}`).join(' ')
  const formatAxis = (value: number) => mode === 'network' ? axisBytes(value) : `${Math.round(value)}`
  const labels = points.length
    ? [0, Math.floor((points.length - 1) / 2), points.length - 1]
        .filter((index, position, all) => all.indexOf(index) === position)
        .map((index) => ({ index, text: pointLabel(points[index].time, range) }))
    : []

  const series = mode === 'network'
    ? [
        { key: 'download' as const, className: 'traffic-line-download', legendClass: 'traffic-legend-download', label: '下载' },
        { key: 'upload' as const, className: 'traffic-line-upload', legendClass: 'traffic-legend-upload', label: '上传' },
      ]
    : [
        { key: 'tcp_connections' as const, className: 'traffic-line-tcp', legendClass: 'traffic-legend-tcp', label: 'TCP' },
        { key: 'udp_connections' as const, className: 'traffic-line-udp', legendClass: 'traffic-legend-udp', label: 'UDP' },
      ]

  const svgRef = useRef<SVGSVGElement>(null)
  const [hoveredIndex, setHoveredIndex] = useState<number | null>(null)
  const activeIndex = hoveredIndex === null || points.length === 0 ? null : Math.min(hoveredIndex, points.length - 1)
  const activePoint = activeIndex === null ? null : points[activeIndex]
  const choosePoint = (clientX: number) => {
    const rect = svgRef.current?.getBoundingClientRect()
    if (!rect || points.length === 0) return
    const plotX = ((clientX - rect.left) / rect.width) * width
    const raw = ((plotX - left) / chartWidth) * (points.length - 1)
    setHoveredIndex(Math.max(0, Math.min(points.length - 1, Math.round(raw))))
  }
  const tooltipWidth = mode === 'network' ? 174 : 150
  const tooltipHeight = mode === 'network' ? 80 : 66
  const tooltipX = activeIndex === null ? 0 : Math.max(left, Math.min(width - right - tooltipWidth, x(activeIndex) - tooltipWidth / 2))
  const tooltipY = top + 8
  const valueLabel = (point: TrafficPoint, key: 'upload' | 'download' | 'tcp_connections' | 'udp_connections') => {
    const value = valueFor(point, key)
    return mode === 'network' ? `${formatBytes(value)}/s` : `${Math.round(value)}`
  }
  return (
    <div className="traffic-chart">
      <svg
        ref={svgRef}
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label={mode === 'network' ? '上传下载流量趋势图' : 'TCP UDP 连接数趋势图'}
        onMouseMove={(event) => choosePoint(event.clientX)}
        onMouseLeave={() => setHoveredIndex(null)}
        onTouchMove={(event) => {
          const touch = event.touches[0]
          if (touch) choosePoint(touch.clientX)
        }}
      >
        {[0, 0.25, 0.5, 0.75, 1].map((ratio) => {
          const gridY = top + chartHeight * ratio
          const value = max * (1 - ratio)
          return (
            <g key={ratio}>
              <line x1={left} x2={width - right} y1={gridY} y2={gridY} className="traffic-grid-line" />
              <text x={left - 8} y={gridY + 4} textAnchor="end" className="traffic-axis-label">{formatAxis(value)}</text>
            </g>
          )
        })}
        {points.length > 0 && series.map((item) => (
          <polyline key={item.key} points={line(item.key)} className={`traffic-line ${item.className}`} />
        ))}
        {labels.map(({ index, text }) => (
          <text key={index} x={x(index)} y={height - 10} textAnchor="middle" className="traffic-axis-label">{text}</text>
        ))}
        {activeIndex !== null && activePoint && (
          <g className="traffic-tooltip" pointerEvents="none">
            <line x1={x(activeIndex)} x2={x(activeIndex)} y1={top} y2={top + chartHeight} className="traffic-crosshair" />
            <circle cx={x(activeIndex)} cy={y(valueFor(activePoint, series[0].key))} r="4" className={`traffic-point ${series[0].className}`} />
            <circle cx={x(activeIndex)} cy={y(valueFor(activePoint, series[1].key))} r="4" className={`traffic-point ${series[1].className}`} />
            <rect x={tooltipX} y={tooltipY} width={tooltipWidth} height={tooltipHeight} rx="8" className="traffic-tooltip-box" />
            <text x={tooltipX + 12} y={tooltipY + 19} className="traffic-tooltip-time">{new Date(activePoint.time).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}</text>
            <text x={tooltipX + 12} y={tooltipY + 43} className="traffic-tooltip-value">{series[0].label}　{valueLabel(activePoint, series[0].key)}</text>
            <text x={tooltipX + 12} y={tooltipY + 64} className="traffic-tooltip-value">{series[1].label}　{valueLabel(activePoint, series[1].key)}</text>
          </g>
        )}
      </svg>
      <div className="traffic-legend">
        {series.map((item) => <span key={item.key}><i className={item.legendClass} />{item.label}</span>)}
      </div>
    </div>
  )
}

export default function Traffic() {
  const screens = Grid.useBreakpoint()
  const compact = !screens.sm
  const [servers, setServers] = useState<Server[]>([])
  const [serverId, setServerId] = useState<number | null>(null)
  const [range, setRange] = useState<TrafficRange>('15m')
  const [selectedInbound, setSelectedInbound] = useState<number>(0)
  const [data, setData] = useState<TrafficSeries | null>(null)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    listServers()
      .then((items) => {
        setServers(items)
        const first = items.find((server) => server.online) ?? items[0]
        if (first) setServerId(first.id)
      })
      .catch((error) => message.error(errMsg(error)))
  }, [])

  const load = useCallback(async (quiet = false) => {
    if (!serverId) return
    if (!quiet) setLoading(true)
    try {
      setData(await getServerTraffic(serverId, range))
    } catch (error) {
      if (!quiet) message.error(errMsg(error))
    } finally {
      if (!quiet) setLoading(false)
    }
  }, [range, serverId])

  useEffect(() => {
    setData(null)
    setSelectedInbound(0)
    void load()
    if (!serverId) return undefined
    const timer = window.setInterval(() => void load(true), 10_000)
    return () => window.clearInterval(timer)
  }, [load, serverId])

  const selectedServer = servers.find((server) => server.id === serverId)
  const selectedPort = data?.ports.find((port) => port.inbound_id === selectedInbound)
  const chartPoints = selectedPort?.points ?? data?.points ?? []
  const hasData = chartPoints.some((point) => point.upload > 0 || point.download > 0)

  return (
    <Card
      title="流量统计"
      size={compact ? 'small' : 'default'}
      extra={<Button icon={<ReloadOutlined />} loading={loading} onClick={() => void load()} title="刷新" aria-label="刷新" />}
      styles={{ body: { padding: compact ? '14px 12px 18px' : '18px 20px 24px' } }}
    >
      <div className="traffic-toolbar">
        <Select
          value={serverId ?? undefined}
          onChange={setServerId}
          placeholder="选择服务器"
          options={servers.map((server) => ({
            value: server.id,
            label: `${server.name}${server.region ? ` · ${server.region}` : ''}${server.online ? '' : '（离线）'}`,
          }))}
          style={{ width: compact ? '100%' : 280 }}
        />
        <Segmented<TrafficRange> options={RANGE_OPTIONS} value={range} onChange={setRange} block={compact} />
        {selectedServer ? <Tag color={selectedServer.online ? 'success' : 'warning'}>{selectedServer.online ? '在线' : '离线'}</Tag> : null}
      </div>

      {!serverId ? (
        <Empty description={servers.length ? '请选择服务器' : '暂无服务器'} />
      ) : !data ? (
        <div className="traffic-empty">{loading ? '正在读取流量数据...' : '暂无流量数据'}</div>
      ) : (
        <>
          {!data.available && !hasData ? (
            <div className="traffic-unavailable">
              <strong>暂未采集到流量</strong>
              <span>请让节点重新连接 Agent，或重新下发一次面板配置以启用统计接口。</span>
            </div>
          ) : null}

          <div className="traffic-dual-grid">
            <section className="traffic-panel-card">
              <div className="traffic-panel-heading">
                <Typography.Title level={5}>网络</Typography.Title>
                <span>↑ {rate(data.upload_rate)}　↓ {rate(data.download_rate)}</span>
              </div>
              <div className="traffic-panel-label">速度</div>
              <TrafficTrend points={chartPoints} range={range} mode="network" stepSeconds={data.step_seconds} />
            </section>
            <section className="traffic-panel-card">
              <div className="traffic-panel-heading">
                <Typography.Title level={5}>连接</Typography.Title>
                <span>TCP: {data.tcp_connections} · UDP: {data.udp_connections}</span>
              </div>
              <div className="traffic-panel-label">连接数</div>
              <TrafficTrend points={data.points} range={range} mode="connections" stepSeconds={data.step_seconds} />
            </section>
          </div>

          <div className="traffic-section-heading traffic-port-heading">
            <div>
              <Typography.Title level={5}>端口流量</Typography.Title>
              <Typography.Text type="secondary" className="traffic-port-hint">点击端口行查看对应趋势</Typography.Text>
            </div>
            <Space size={10} wrap className="traffic-port-actions">
              {data.updated_at ? <Typography.Text type="secondary">更新于 {new Date(data.updated_at).toLocaleTimeString('zh-CN', { hour12: false })}</Typography.Text> : null}
              <Select
                size="small"
                value={selectedInbound}
                onChange={setSelectedInbound}
                options={[{ value: 0, label: '全部端口' }, ...data.ports.map((port) => ({ value: port.inbound_id, label: `${port.port} · ${port.tag}` }))]}
                style={{ minWidth: 150 }}
              />
            </Space>
          </div>
          <Table
            className="traffic-port-table"
            rowKey="inbound_id"
            size="small"
            pagination={false}
            tableLayout="fixed"
            scroll={{ x: 760 }}
            dataSource={data.ports}
            locale={{ emptyText: '暂无入站端口' }}
            rowClassName={(record) => record.inbound_id === selectedInbound ? 'traffic-port-selected' : ''}
            onRow={(record) => ({ onClick: () => setSelectedInbound(record.inbound_id), style: { cursor: 'pointer' } })}
            columns={[
              { title: '端口', dataIndex: 'port', width: 80 },
              { title: '标签', dataIndex: 'tag', width: 260, ellipsis: true },
              { title: '协议', dataIndex: 'type', width: 130, render: (value: string) => <Tag>{value}</Tag> },
              { title: '下载', dataIndex: 'download', width: 130, render: (_: number, record) => formatBytes(record.download) },
              { title: '上传', dataIndex: 'upload', width: 130, render: (_: number, record) => formatBytes(record.upload) },
              { title: '合计', width: 130, render: (_: unknown, record) => formatBytes(record.upload + record.download) },
            ]}
          />
        </>
      )}
    </Card>
  )
}
