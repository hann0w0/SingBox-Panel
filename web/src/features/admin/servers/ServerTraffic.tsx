import { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type React from 'react'
import { Button, Card, Grid, Segmented, Space, Table, Tag, Tooltip, Typography, message } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { errMsg, getServerTraffic, isCanceledRequest } from '../../../api'
import type { TrafficPoint, TrafficPortSeries, TrafficRange, TrafficSeries } from '../../../types'
import { formatBytes } from '../../../util'
import { useSSE } from '../../../useSSE'
import type { SSEMessage } from '../../../useSSE'

function rate(n: number): string {
  return `${formatBytes(n)}/s`
}

const SUB_DAY_RANGES: TrafficRange[] = ['15m', '30m', '1h', '12h', '24h']

function isDailyRange(range: TrafficRange): boolean {
  return !SUB_DAY_RANGES.includes(range)
}

function pointLabel(time: string, range: TrafficRange): string {
  const date = new Date(time)
  if (Number.isNaN(date.getTime())) return ''
  if (range === '15m') {
    return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  }
  if (!isDailyRange(range)) {
    return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
  }
  return date.toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' })
}

function axisBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  if (n < 1024) return `${Math.round(n)} B`
  return formatBytes(n)
}

const TRAFFIC_SERIES = [
  { key: 'download' as const, className: 'traffic-line-download', legendClass: 'traffic-legend-download', label: '下载' },
  { key: 'upload' as const, className: 'traffic-line-upload', legendClass: 'traffic-legend-upload', label: '上传' },
]

// Keep the traffic table's protocol tags visually aligned with the node
// assignment and management views.
const PROTOCOL_TAGS: Record<string, { label: string; color: string }> = {
  vless: { label: 'VLESS', color: 'blue' },
  vmess: { label: 'VMess', color: 'purple' },
  ss: { label: 'SS', color: 'green' },
  shadowsocks: { label: 'SS', color: 'green' },
  trojan: { label: 'Trojan', color: 'geekblue' },
  hysteria2: { label: 'Hysteria2', color: 'cyan' },
  hysteria: { label: 'Hysteria', color: 'cyan' },
  tuic: { label: 'TUIC', color: 'orange' },
  anytls: { label: 'AnyTLS', color: 'magenta' },
  socks: { label: 'SOCKS', color: 'default' },
  socks5: { label: 'SOCKS5', color: 'default' },
  snell: { label: 'Snell', color: 'gold' },
  mixed: { label: 'Mixed', color: 'volcano' },
}

export function ProtocolTag({ type }: { type: string }) {
  const protocol = type.trim().toLowerCase()
  const tag = PROTOCOL_TAGS[protocol]
  return <Tag color={tag?.color}>{tag?.label || type}</Tag>
}

type TrafficChartMode = 'rate' | 'usage'

function chartLabelCount(range: TrafficRange, width: number): number {
  const compact = width < 560
  if (range === '15m') return compact ? 5 : 9
  if (range === '24h') return compact ? 6 : 12
  if (range === '7d') return compact ? 4 : 7
  return compact ? 5 : 10
}

function tooltipTime(time: string, range: TrafficRange): string {
  const date = new Date(time)
  if (Number.isNaN(date.getTime())) return ''
  if (range === '15m') {
    return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  }
  if (isDailyRange(range)) {
    return date.toLocaleDateString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', weekday: 'short' })
  }
  return date.toLocaleString('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit',
  })
}

// TrafficTrend renders one SVG area chart. Memoized: geometry, axes and path
// data are recomputed only when points/width/step/range change; mouse hover
// only re-renders the tooltip overlay.
export const TrafficTrend = memo(function TrafficTrend({
  points,
  range,
  stepSeconds,
  mode,
}: {
  points: TrafficPoint[]
  range: TrafficRange
  stepSeconds: number
  mode: TrafficChartMode
}) {
  const containerRef = useRef<HTMLDivElement>(null)
  const svgRef = useRef<SVGSVGElement>(null)
  const [width, setWidth] = useState(1)
  const [hoveredIndex, setHoveredIndex] = useState<number | null>(null)

  useEffect(() => {
    const el = containerRef.current
    if (!el) return
    const measure = () => setWidth(Math.max(1, Math.floor(el.getBoundingClientRect().width || el.clientWidth)))
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    return () => observer.disconnect()
  }, [])

  const height = 250
  const left = width < 260 ? 40 : 52
  const right = width < 260 ? 8 : 16
  const top = 18
  const bottom = 34
  const chartWidth = Math.max(1, width - left - right)
  const chartHeight = height - top - bottom

  const chart = useMemo(() => {
    const valueFor = (point: TrafficPoint, key: 'upload' | 'download') => {
      if (mode === 'usage') return point[key]
      const sampledRate = key === 'upload' ? point.upload_rate : point.download_rate
      return sampledRate > 0 ? sampledRate : point[key] / Math.max(1, stepSeconds)
    }
    const max = Math.max(1, ...points.flatMap((p) => [valueFor(p, 'upload'), valueFor(p, 'download')]))
    const x = (index: number) => left + (points.length <= 1 ? chartWidth / 2 : (index / (points.length - 1)) * chartWidth)
    const y = (value: number) => top + chartHeight - (value / max) * chartHeight
    const line = (key: 'upload' | 'download') =>
      points.map((point, index) => `${x(index)},${y(valueFor(point, key))}`).join(' ')
    const labelCount = Math.min(points.length, chartLabelCount(range, width))
    const labelIndices = points.length > 1
      ? Array.from({ length: labelCount }, (_, i) => Math.round((i / Math.max(1, labelCount - 1)) * (points.length - 1)))
      : [0]
    const labels = points.length
      ? [...new Set(labelIndices)]
          .filter((i) => i < points.length)
          .map((index) => ({ index, text: pointLabel(points[index].time, range) }))
      : []
    const gridLines = [0, 0.25, 0.5, 0.75, 1].map((ratio) => {
      const gridY = top + chartHeight * ratio
      return { key: ratio, y: gridY, value: max * (1 - ratio) }
    })
    return { valueFor, max, x, y, line, labels, gridLines }
  }, [points, range, stepSeconds, mode, width, left, top, chartWidth, chartHeight])

  const activeIndex = hoveredIndex === null || points.length === 0 ? null : Math.min(hoveredIndex, points.length - 1)
  const activePoint = activeIndex === null ? null : points[activeIndex]

  const choosePoint = useCallback((clientX: number) => {
    const rect = svgRef.current?.getBoundingClientRect()
    if (!rect || rect.width <= 0 || points.length === 0) return
    const plotX = ((clientX - rect.left) / rect.width) * width
    const raw = ((plotX - left) / chartWidth) * (points.length - 1)
    setHoveredIndex(Math.max(0, Math.min(points.length - 1, Math.round(raw))))
  }, [points.length, width, left, chartWidth])

  const tooltipWidth = Math.min(174, Math.max(96, width - 8))
  const tooltipHeight = 80
  const tooltipX = activeIndex === null
    ? 0
    : Math.max(4, Math.min(Math.max(4, width - tooltipWidth - 4), chart.x(activeIndex) - tooltipWidth / 2))
  const tooltipY = top + 8
  const valueLabel = (point: TrafficPoint, key: 'upload' | 'download') =>
    `${formatBytes(chart.valueFor(point, key))}${mode === 'rate' ? '/s' : ''}`

  return (
    <div className="traffic-chart" ref={containerRef}>
      <svg
        ref={svgRef}
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label={mode === 'rate' ? '上传下载实时速度趋势图' : '上传下载历史流量趋势图'}
        onMouseMove={(event) => choosePoint(event.clientX)}
        onMouseLeave={() => setHoveredIndex(null)}
        onTouchMove={(event) => {
          const touch = event.touches[0]
          if (touch) choosePoint(touch.clientX)
        }}
      >
        {chart.gridLines.map(({ key, y: gridY, value }) => (
          <g key={key}>
            <line x1={left} x2={width - right} y1={gridY} y2={gridY} className="traffic-grid-line" />
            <text x={left - 8} y={gridY + 4} textAnchor="end" className="traffic-axis-label">{axisBytes(value)}</text>
          </g>
        ))}
        {points.length > 0 && TRAFFIC_SERIES.map((item) => (
          <polyline key={item.key} points={chart.line(item.key)} className={`traffic-line ${item.className}`} />
        ))}
        {chart.labels.map(({ index, text }) => (
          <text key={index} x={chart.x(index)} y={height - 10} textAnchor="middle" className="traffic-axis-label">{text}</text>
        ))}
        {activeIndex !== null && activePoint && (
          <g className="traffic-tooltip" pointerEvents="none">
            <line x1={chart.x(activeIndex)} x2={chart.x(activeIndex)} y1={top} y2={top + chartHeight} className="traffic-crosshair" />
            <circle cx={chart.x(activeIndex)} cy={chart.y(chart.valueFor(activePoint, TRAFFIC_SERIES[0].key))} r="4" className={`traffic-point ${TRAFFIC_SERIES[0].className}`} />
            <circle cx={chart.x(activeIndex)} cy={chart.y(chart.valueFor(activePoint, TRAFFIC_SERIES[1].key))} r="4" className={`traffic-point ${TRAFFIC_SERIES[1].className}`} />
            <rect x={tooltipX} y={tooltipY} width={tooltipWidth} height={tooltipHeight} rx="8" className="traffic-tooltip-box" />
            <text x={tooltipX + 12} y={tooltipY + 19} className="traffic-tooltip-time">{tooltipTime(activePoint.time, range)}</text>
            <text x={tooltipX + 12} y={tooltipY + 43} className="traffic-tooltip-value">{TRAFFIC_SERIES[0].label}　{valueLabel(activePoint, TRAFFIC_SERIES[0].key)}</text>
            <text x={tooltipX + 12} y={tooltipY + 64} className="traffic-tooltip-value">{TRAFFIC_SERIES[1].label}　{valueLabel(activePoint, TRAFFIC_SERIES[1].key)}</text>
          </g>
        )}
      </svg>
      <div className="traffic-legend">
        {TRAFFIC_SERIES.map((item) => <span key={item.key}><i className={item.legendClass} />{item.label}</span>)}
      </div>
    </div>
  )
})

// LiveChart owns its own SSE subscription and rolling window so a 3s traffic
// sample only re-renders this panel, never the historical chart or port table.
const LiveChart = memo(function LiveChart({ serverId }: { serverId: number }) {
  const [livePoints, setLivePoints] = useState<TrafficPoint[]>([])
  const liveUrl = useMemo(() => `/api/admin/servers/${serverId}/traffic/live`, [serverId])

  const onSSEMessage = useCallback((msg: SSEMessage) => {
    if (msg.kind !== 'traffic' || !msg.data) return
    const snap = msg.data as any
    setLivePoints((prev) => {
      const next = prev.length >= 100 ? prev.slice(prev.length - 99) : prev.slice()
      next.push({
        time: new Date(msg.ts).toISOString(),
        upload: 0, download: 0,
        upload_rate: snap.upload_rate || 0,
        download_rate: snap.download_rate || 0,
        tcp_connections: 0, udp_connections: 0,
      })
      return next
    })
  }, [])

  const { connected } = useSSE(liveUrl, true, onSSEMessage)
  const liveRate = livePoints.length > 0 ? livePoints[livePoints.length - 1] : null

  return (
    <section className="traffic-panel-card">
      <div className="traffic-panel-heading">
        <Typography.Title level={5}>实时</Typography.Title>
        <span>
          ↑ {rate(liveRate?.upload_rate ?? 0)}　
          ↓ {rate(liveRate?.download_rate ?? 0)}
          {connected ? '' : ' · 连接中…'}
        </span>
      </div>
      <div className="traffic-panel-label">速度</div>
      <TrafficTrend points={livePoints} range="15m" stepSeconds={3} mode="rate" />
    </section>
  )
})

const RANGE_OPTIONS: { label: string; value: TrafficRange }[] = [
  { label: '1 小时', value: '1h' },
  { label: '12 小时', value: '12h' },
  { label: '24 小时', value: '24h' },
  { label: '7 天', value: '7d' },
  { label: '30 天', value: '30d' },
]

type PortRow = Pick<TrafficPortSeries, 'inbound_id' | 'tag' | 'port' | 'type' | 'upload' | 'download'>

const bytesSorter = <T extends { upload: number; download: number }>(key: 'upload' | 'download' | 'total') =>
  (a: T, b: T) => (key === 'total' ? a.upload + a.download - (b.upload + b.download) : a[key] - b[key])

// ServerTraffic is the per-server traffic tab: live rate, history and the
// per-inbound breakdown for one node.
export default function ServerTraffic({ serverId, titleExtra }: { serverId: number; titleExtra?: React.ReactNode }) {
  const screens = Grid.useBreakpoint()
  // Below antd's md breakpoint the controls leave the card header, which is
  // too narrow for a selector plus a five-way segmented control.
  const compact = !screens.md
  const [data, setData] = useState<TrafficSeries | null>(null)
  const [loading, setLoading] = useState(false)
  const [range, setRange] = useState<TrafficRange>('24h')
  const loadAbortRef = useRef<AbortController | null>(null)
  const loadGenerationRef = useRef(0)

  const load = useCallback(async (quiet = false) => {
    loadAbortRef.current?.abort()
    const controller = new AbortController()
    const generation = ++loadGenerationRef.current
    loadAbortRef.current = controller
    if (!quiet) setLoading(true)
    try {
      const next = await getServerTraffic(serverId, range, controller.signal)
      if (generation === loadGenerationRef.current) setData(next)
    } catch (error) {
      if (!quiet && !isCanceledRequest(error)) message.error(errMsg(error))
    } finally {
      if (generation === loadGenerationRef.current) {
        loadAbortRef.current = null
        if (!quiet) setLoading(false)
      }
    }
  }, [range, serverId])

  useEffect(() => {
    setData(null)
    void load()
    const timer = window.setInterval(() => void load(true), 60000)
    return () => window.clearInterval(timer)
  }, [load])

  useEffect(() => () => {
    loadGenerationRef.current += 1
    loadAbortRef.current?.abort()
  }, [])

  const portRows: PortRow[] = useMemo(() => {
    if (!data) return []
    return data.ports.map((p) => ({
      inbound_id: p.inbound_id, tag: p.tag, port: p.port, type: p.type, upload: p.upload, download: p.download,
    }))
  }, [data])
  const unattributedUpload = data?.unattributed_upload ?? 0
  const unattributedDownload = data?.unattributed_download ?? 0

  return (
    <Card
      title="流量"
      extra={
        <span style={{ display: 'inline-flex', gap: 8, alignItems: 'center' }}>
          {!compact && titleExtra}
          {!compact && <Segmented<TrafficRange> options={RANGE_OPTIONS} value={range} onChange={setRange} style={{ flex: 'none' }} />}
          <Button
            size={compact ? 'small' : 'middle'}
            icon={<ReloadOutlined />}
            loading={loading}
            onClick={() => void load()}
            title="刷新"
            aria-label="刷新"
            style={{ flex: 'none' }}
          />
        </span>
      }
    >
      {compact && (
        <div className="traffic-toolbar" style={{ marginBottom: 12 }}>
          {titleExtra}
          <Segmented<TrafficRange> block size="small" options={RANGE_OPTIONS} value={range} onChange={setRange} />
        </div>
      )}
      {data?.available && (
        <div style={{ marginBottom: 16 }}>
          <Space size="large" wrap>
            <span>上传 ↑ <b style={{ color: 'var(--console-upload)' }}>{formatBytes(data.upload)}</b></span>
            <span>下载 ↓ <b style={{ color: 'var(--console-download)' }}>{formatBytes(data.download)}</b></span>
            <span>合计 <b>{formatBytes(data.upload + data.download)}</b></span>
          </Space>
          <div style={{ marginTop: 4 }}>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              所选时段内此主机的 sing-box 总用量，包含共用凭据节点和多用户用量。
            </Typography.Text>
          </div>
        </div>
      )}
      <div className="traffic-dual-grid" style={{ marginTop: 0 }}>
        <LiveChart key={serverId} serverId={serverId} />
        <section className="traffic-panel-card">
          <div className="traffic-panel-heading">
            <Typography.Title level={5}>历史</Typography.Title>
            {data?.timezone && isDailyRange(range) ? (
              <span style={{ fontSize: 12 }}>按 {data.timezone} 自然日统计</span>
            ) : null}
          </div>
          <div className="traffic-panel-label">流量使用量</div>
          {data ? (
            <TrafficTrend points={data.points} range={range} stepSeconds={data.step_seconds} mode="usage" />
          ) : (
            <div className="traffic-empty">{loading ? '读取中...' : '暂无历史数据'}</div>
          )}
        </section>
      </div>

      {data && data.available ? (
        <Table<PortRow>
          className="traffic-port-table"
          rowKey="inbound_id"
          size="small"
          pagination={false}
          tableLayout="fixed"
          scroll={{ x: compact ? 620 : 760 }}
          dataSource={portRows}
          locale={{ emptyText: '暂无入站端口' }}
          columns={[
            { title: '端口', dataIndex: 'port', width: compact ? 68 : 80, sorter: (a, b) => a.port - b.port },
            { title: '标签', dataIndex: 'tag', width: compact ? 120 : 180, ellipsis: true },
            { title: '协议', dataIndex: 'type', width: compact ? 92 : 100, render: (value: string) => <ProtocolTag type={value} /> },
            { title: '下行 ↓', dataIndex: 'download', width: compact ? 104 : 130, sorter: bytesSorter('download'), render: (v: number) => <span style={{ color: 'var(--console-download)' }}>{formatBytes(v)}</span> },
            { title: '上行 ↑', dataIndex: 'upload', width: compact ? 104 : 130, sorter: bytesSorter('upload'), render: (v: number) => <span style={{ color: 'var(--console-upload)' }}>{formatBytes(v)}</span> },
            {
              title: '合计', width: compact ? 110 : 150, sorter: bytesSorter('total'), defaultSortOrder: 'descend',
              render: (_: unknown, r) => <span style={{ fontWeight: 600 }}>{formatBytes(r.upload + r.download)}</span>,
            },
          ]}
          summary={() => (unattributedUpload + unattributedDownload > 0 ? (
            <Table.Summary fixed>
              <Table.Summary.Row>
                <Table.Summary.Cell index={0}>—</Table.Summary.Cell>
                <Table.Summary.Cell index={1}>
                  <Tooltip title="节点总量中无法归属到具体入站的部分：不由面板管理的入站；sing-box 低于 1.14 时还包括采样间隔内建立又关闭的连接">
                    <span style={{ color: 'var(--console-muted)' }}>未归属 ⓘ</span>
                  </Tooltip>
                </Table.Summary.Cell>
                <Table.Summary.Cell index={2}>—</Table.Summary.Cell>
                <Table.Summary.Cell index={3}><span style={{ color: 'var(--console-download)' }}>{formatBytes(unattributedDownload)}</span></Table.Summary.Cell>
                <Table.Summary.Cell index={4}><span style={{ color: 'var(--console-upload)' }}>{formatBytes(unattributedUpload)}</span></Table.Summary.Cell>
                <Table.Summary.Cell index={5}><span style={{ fontWeight: 600 }}>{formatBytes(unattributedUpload + unattributedDownload)}</span></Table.Summary.Cell>
              </Table.Summary.Row>
            </Table.Summary>
          ) : null)}
        />
      ) : data && !data.available ? (
        <div className="traffic-unavailable">
          <strong>暂未采集到流量</strong>
          <span>请让节点重新连接 Agent，或重新下发一次面板配置以启用统计接口。</span>
        </div>
      ) : null}
    </Card>
  )
}
