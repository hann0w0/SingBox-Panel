import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button, Card, Empty, Grid, Segmented, Select, Table, Tag, Typography, message } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { errMsg, getServerTraffic, listServers } from '../../api'
import type { Server, TrafficPoint, TrafficRange, TrafficSeries } from '../../types'
import { formatBytes } from '../../util'
import { useSSE } from '../../useSSE'
import type { SSEMessage } from '../../useSSE'

function rate(n: number): string {
  return `${formatBytes(n)}/s`
}

function pointLabel(time: string, range: TrafficRange): string {
  const date = new Date(time)
  if (Number.isNaN(date.getTime())) return ''
  if (range === '15m') {
    return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  }
  if (range === '24h') {
    return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
  }
  return date.toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' })
}

function axisBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  if (n < 1024) return `${Math.round(n)} B`
  return formatBytes(n)
}

function TrafficTrend({ points, range, stepSeconds }: { points: TrafficPoint[]; range: TrafficRange; stepSeconds: number }) {
    const containerRef = useRef<HTMLDivElement>(null)
    const [width, setWidth] = useState(760)

    useEffect(() => {
      const el = containerRef.current
      if (!el) return
      const measure = () => setWidth(Math.max(300, el.clientWidth))
      measure()
      const observer = new ResizeObserver(measure)
      observer.observe(el)
      return () => observer.disconnect()
    }, [])

    const height = 250
  const left = 52
  const right = 16
  const top = 18
  const bottom = 34
  const chartWidth = width - left - right
  const chartHeight = height - top - bottom
  const valueFor = (point: TrafficPoint, key: 'upload' | 'download') => {
    const sampledRate = key === 'upload' ? point.upload_rate : point.download_rate
    return sampledRate > 0 ? sampledRate : point[key] / Math.max(1, stepSeconds)
  }
  const max = Math.max(1, ...points.flatMap((p) => [valueFor(p, 'upload'), valueFor(p, 'download')]))
  const x = (index: number) => left + (points.length <= 1 ? chartWidth / 2 : (index / (points.length - 1)) * chartWidth)
  const y = (value: number) => top + chartHeight - (value / max) * chartHeight
  const line = (key: 'upload' | 'download') =>
    points.map((point, index) => `${x(index)},${y(valueFor(point, key))}`).join(' ')
  const formatAxis = (value: number) => axisBytes(value)
  const labelCount = range === '7d' ? 7 : range === '30d' ? 10 : range === '24h' ? 6 : 5
  const labelIndices = points.length > 1
    ? Array.from({ length: labelCount }, (_, i) => Math.round((i / (labelCount - 1)) * (points.length - 1)))
    : [0]
  const labels = points.length
    ? [...new Set(labelIndices)]
        .filter(i => i < points.length)
        .map((index) => ({ index, text: pointLabel(points[index].time, range) }))
    : []

  const series = [
      { key: 'download' as const, className: 'traffic-line-download', legendClass: 'traffic-legend-download', label: '下载' },
      { key: 'upload' as const, className: 'traffic-line-upload', legendClass: 'traffic-legend-upload', label: '上传' },
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
  const tooltipWidth = 174
  const tooltipHeight = 80
  const tooltipX = activeIndex === null ? 0 : Math.max(left, Math.min(width - right - tooltipWidth, x(activeIndex) - tooltipWidth / 2))
  const tooltipY = top + 8
  const valueLabel = (point: TrafficPoint, key: 'upload' | 'download') => `${formatBytes(valueFor(point, key))}/s`
  return (
    <div className="traffic-chart" ref={containerRef}>
      <svg
        ref={svgRef}
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label={'上传下载流量趋势图'}
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
  const [data, setData] = useState<TrafficSeries | null>(null)
  const [loading, setLoading] = useState(false)

  // SSE live traffic samples — always active when a server is selected.
  const [livePoints, setLivePoints] = useState<TrafficPoint[]>([])
  const liveUrl = useMemo(() => `/api/admin/servers/${serverId}/traffic/live`, [serverId])
  const onSSEMessage = useCallback((msg: SSEMessage) => {
    if (msg.kind !== 'traffic' || !msg.data) return
    const snap = msg.data as any
    const point: TrafficPoint = {
      time: new Date(msg.ts).toISOString(),
      upload: 0, download: 0,
      upload_rate: snap.upload_rate || 0,
      download_rate: snap.download_rate || 0,
      tcp_connections: snap.tcp_connections || 0,
      udp_connections: snap.udp_connections || 0,
    }
    setLivePoints((prev) => {
      const next = [...prev, point]
      if (next.length > 100) next.shift()
      return next
    })
  }, [])
  const { connected: sseConnected } = useSSE(liveUrl, !!serverId, onSSEMessage)
  const [range, setRange] = useState<TrafficRange>('24h')

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
    void load()
  }, [load, serverId])

  const selectedServer = servers.find((server) => server.id === serverId)
  const liveChartPoints = livePoints
  const historicalChartPoints = data?.points ?? []
  const hasData = liveChartPoints.length > 0 || historicalChartPoints.some((p) => p.upload > 0 || p.download > 0)
  const liveRate = livePoints.length > 0 ? livePoints[livePoints.length - 1] : null

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
        <Segmented<TrafficRange> options={[
          { label: '24 小时', value: '24h' },
          { label: '7 天', value: '7d' },
          { label: '30 天', value: '30d' },
        ]} value={range} onChange={setRange} />
      </div>

      {!serverId ? (
        <Empty description={servers.length ? '请选择服务器' : '暂无服务器'} />
      ) : (
        <>
          <div className="traffic-dual-grid">
            <section className="traffic-panel-card">
              <div className="traffic-panel-heading">
                <Typography.Title level={5}>实时</Typography.Title>
                <span>
                  ↑ {rate(liveRate?.upload_rate ?? 0)}　
                  ↓ {rate(liveRate?.download_rate ?? 0)}
                  {sseConnected ? '' : ' · 连接中…'}
                </span>
              </div>
              <div className="traffic-panel-label">速度</div>
              <TrafficTrend points={liveChartPoints} range="15m" stepSeconds={3} />
            </section>
            <section className="traffic-panel-card">
              <div className="traffic-panel-heading">
                <Typography.Title level={5}>历史</Typography.Title>
              </div>
              <div className="traffic-panel-label">速度</div>
              {data ? (
                <TrafficTrend points={historicalChartPoints} range={range} stepSeconds={data.step_seconds} />
              ) : (
                <div className="traffic-empty">{loading ? '读取中...' : '暂无历史数据'}</div>
              )}
            </section>
          </div>

          {data && data.available ? (
            <>
              <Table
                className="traffic-port-table"
                rowKey="inbound_id"
                size="small"
                pagination={false}
                tableLayout="fixed"
                scroll={{ x: 760 }}
                dataSource={data.ports}
                locale={{ emptyText: '暂无入站端口' }}
                rowClassName={() => ''}
                onRow={() => ({ style: { cursor: 'pointer' } })}
                columns={[
                  { title: '端口', dataIndex: 'port', width: 80 },
                  { title: '标签', dataIndex: 'tag', width: 180, ellipsis: true },
                  { title: '协议', dataIndex: 'type', width: 90, render: (value: string) => <Tag>{value}</Tag> },
                  { title: '出站 ↓', dataIndex: 'download', width: 130, render: (_: number, record) => <span style={{ color: '#4096ff' }}>{formatBytes(record.download)}</span> },
                  { title: '入站 ↑', dataIndex: 'upload', width: 130, render: (_: number, record) => <span style={{ color: '#36cfc9' }}>{formatBytes(record.upload)}</span> },
                  { title: '全部', width: 120, render: (_: unknown, record) => <span style={{ fontWeight: 600 }}>{formatBytes(record.upload + record.download)}</span> },
                ]}
              />
            </>
          ) : data && !data.available ? (
            <div className="traffic-unavailable">
              <strong>暂未采集到流量</strong>
              <span>请让节点重新连接 Agent，或重新下发一次面板配置以启用统计接口。</span>
            </div>
          ) : null}
        </>
      )}
    </Card>
  )
}
