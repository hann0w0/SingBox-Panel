import { memo, useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { Grid, Segmented, Select, message } from '../../../ui'
import { errMsg, getServerTraffic, isCanceledRequest } from '../../../api'
import type { Server, TrafficPoint, TrafficRange, TrafficSeries } from '../../../types'
import { formatBytes } from '../../../util'
import { useSSE } from '../../../useSSE'
import type { SSEMessage } from '../../../useSSE'
import { Stats } from '../../../ui/Page'
import '../../../styles/traffic-logs.css'

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
    return date.toLocaleTimeString('zh-CN', { timeZone: 'UTC', hour: '2-digit', minute: '2-digit', second: '2-digit' })
  }
  if (!isDailyRange(range)) {
    return date.toLocaleTimeString('zh-CN', { timeZone: 'UTC', hour: '2-digit', minute: '2-digit' })
  }
  return date.toLocaleDateString('zh-CN', { timeZone: 'UTC', month: '2-digit', day: '2-digit' })
}

// Axis labels use at most one decimal so they fit the left gutter.
function axisBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  if (n < 1024) return `${Math.round(n)} B`
  const units = ['KB', 'MB', 'GB', 'TB', 'PB']
  let value = n / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`
}

const TRAFFIC_SERIES = [
  { key: 'download' as const, className: 'tr-line-download', legendClass: 'tr-legend-download', label: '下载' },
  { key: 'upload' as const, className: 'tr-line-upload', legendClass: 'tr-legend-upload', label: '上传' },
]

const PROTOCOL_LABELS: Record<string, string> = {
  vless: 'VLESS',
  vmess: 'VMess',
  ss: 'SS',
  shadowsocks: 'SS',
  trojan: 'Trojan',
  hysteria2: 'Hysteria2',
  hysteria: 'Hysteria',
  tuic: 'TUIC',
  anytls: 'AnyTLS',
  socks: 'SOCKS',
  socks5: 'SOCKS5',
  snell: 'Snell',
  mixed: 'Mixed',
}

// Neutral protocol pill (design A has no coloured tags).
export function ProtocolTag({ type }: { type: string }) {
  const protocol = type.trim().toLowerCase()
  return <span className="ui-tag">{PROTOCOL_LABELS[protocol] || type}</span>
}

function TrafficLegend() {
  return (
    <div className="tr-legend">
      {TRAFFIC_SERIES.map((item) => <span key={item.key}><i className={item.legendClass} />{item.label}</span>)}
    </div>
  )
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
    return date.toLocaleTimeString('zh-CN', { timeZone: 'UTC', hour: '2-digit', minute: '2-digit', second: '2-digit' })
  }
  if (isDailyRange(range)) {
    return date.toLocaleDateString('zh-CN', { timeZone: 'UTC', year: 'numeric', month: '2-digit', day: '2-digit', weekday: 'short' })
  }
  return date.toLocaleString('zh-CN', {
    timeZone: 'UTC',
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
  const left = width < 260 ? 44 : 56
  const right = width < 260 ? 18 : 26
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
    <div className="tr-chart" ref={containerRef}>
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
            <line x1={left} x2={width - right} y1={gridY} y2={gridY} className="tr-grid-line" />
            <text x={left - 8} y={gridY + 4} textAnchor="end" className="tr-axis-label">{axisBytes(value)}</text>
          </g>
        ))}
        {points.length > 1 && (
          <polygon
            points={`${chart.x(0)},${top + chartHeight} ${chart.line('download')} ${chart.x(points.length - 1)},${top + chartHeight}`}
            className="tr-area"
          />
        )}
        {points.length > 0 && TRAFFIC_SERIES.map((item) => (
          <polyline key={item.key} points={chart.line(item.key)} className={`tr-line ${item.className}`} />
        ))}
        {chart.labels.map(({ index, text }) => (
          <text key={index} x={chart.x(index)} y={height - 10} textAnchor="middle" className="tr-axis-label">{text}</text>
        ))}
        {activeIndex !== null && activePoint && (
          <g className="tr-tooltip" pointerEvents="none">
            <line x1={chart.x(activeIndex)} x2={chart.x(activeIndex)} y1={top} y2={top + chartHeight} className="tr-crosshair" />
            <circle cx={chart.x(activeIndex)} cy={chart.y(chart.valueFor(activePoint, TRAFFIC_SERIES[0].key))} r="4" className={`tr-point ${TRAFFIC_SERIES[0].className}`} />
            <circle cx={chart.x(activeIndex)} cy={chart.y(chart.valueFor(activePoint, TRAFFIC_SERIES[1].key))} r="4" className={`tr-point ${TRAFFIC_SERIES[1].className}`} />
            <rect x={tooltipX} y={tooltipY} width={tooltipWidth} height={tooltipHeight} rx="8" className="tr-tooltip-box" />
            <text x={tooltipX + 12} y={tooltipY + 19} className="tr-tooltip-time">{tooltipTime(activePoint.time, range)}</text>
            <text x={tooltipX + 12} y={tooltipY + 43} className="tr-tooltip-value">{TRAFFIC_SERIES[0].label}　{valueLabel(activePoint, TRAFFIC_SERIES[0].key)}</text>
            <text x={tooltipX + 12} y={tooltipY + 64} className="tr-tooltip-value">{TRAFFIC_SERIES[1].label}　{valueLabel(activePoint, TRAFFIC_SERIES[1].key)}</text>
          </g>
        )}
      </svg>
    </div>
  )
})

type LiveRate = { upload: number; download: number; connected: boolean }

// Tiny external store so the headline rate cell can follow LiveChart's SSE
// samples without re-rendering the history chart or the port table.
function createLiveRateStore() {
  let value: LiveRate = { upload: 0, download: 0, connected: false }
  const listeners = new Set<() => void>()
  return {
    get: () => value,
    set: (next: LiveRate) => {
      if (next.upload === value.upload && next.download === value.download && next.connected === value.connected) return
      value = next
      listeners.forEach((listener) => listener())
    },
    subscribe: (listener: () => void) => {
      listeners.add(listener)
      return () => { listeners.delete(listener) }
    },
  }
}
type LiveRateStore = ReturnType<typeof createLiveRateStore>

function LiveRateValue({ store, part }: { store: LiveRateStore; part: 'download' | 'upload' }) {
  const live = useSyncExternalStore(store.subscribe, store.get, store.get)
  if (part === 'upload') return <>{live.connected ? `↑ ${rate(live.upload)}` : '连接中…'}</>
  return <>↓ {rate(live.download)}</>
}

// LiveChart owns its own SSE subscription and rolling window so a 3s traffic
// sample only re-renders this panel, never the historical chart or port table.
const LiveChart = memo(function LiveChart({ serverId, rateStore }: { serverId: number; rateStore: LiveRateStore }) {
  const [livePoints, setLivePoints] = useState<TrafficPoint[]>([])
  const [sample, setSample] = useState<{ point: TrafficPoint; sampledAt: number } | null>(null)
  const [clock, setClock] = useState(() => performance.now())
  const liveUrl = useMemo(() => `/api/admin/servers/${serverId}/traffic/live`, [serverId])

  const onSSEMessage = useCallback((msg: SSEMessage) => {
    if (msg.kind !== 'traffic' || !Number.isFinite(msg.ts)) return
    const date = new Date(msg.ts)
    if (Number.isNaN(date.getTime())) return
    const snap = msg.data as any
    const age = typeof msg.age_ms === 'number' && Number.isFinite(msg.age_ms) && msg.age_ms >= 0 ? msg.age_ms : 0
    const point: TrafficPoint = {
      time: date.toISOString(),
      upload: 0, download: 0,
      upload_rate: snap?.upload_rate || 0,
      download_rate: snap?.download_rate || 0,
      tcp_connections: 0, udp_connections: 0,
    }
    // The panel's age includes backlog/queue time. Local elapsed time uses a
    // monotonic clock, independent of the browser's wall clock or timezone.
    setSample({ point, sampledAt: performance.now() - age })
    setLivePoints((prev) => {
      const next = prev.length >= 100 ? prev.slice(prev.length - 99) : prev.slice()
      next.push(point)
      return next
    })
  }, [])

  const { connected } = useSSE(liveUrl, true, onSSEMessage)

  useEffect(() => {
    const timer = setInterval(() => setClock(performance.now()), 3000)
    return () => clearInterval(timer)
  }, [])

  useEffect(() => {
    // SSE heartbeats only prove the browser can reach the panel. Old backlog
    // or a stalled Agent must not keep a nonzero current speed alive.
    const elapsed = sample ? Math.max(0, Math.max(clock, performance.now()) - sample.sampledAt) : 0
    const fresh = connected && sample && elapsed < 15_000
    rateStore.set({ upload: fresh ? sample.point.upload_rate : 0, download: fresh ? sample.point.download_rate : 0, connected })
    if (sample && !fresh) {
      setLivePoints((points) => {
        const last = points[points.length - 1]
        if (!last || (last.upload_rate === 0 && last.download_rate === 0)) return points
        const zeroTime = new Date(Date.parse(sample.point.time) + elapsed)
        return [...points.slice(-99), {
          // Keep synthetic points in the server's UTC coordinate system, and
          // do not replace the real sample's freshness baseline with them.
          time: Number.isNaN(zeroTime.getTime()) ? sample.point.time : zeroTime.toISOString(),
          upload: 0, download: 0, upload_rate: 0, download_rate: 0,
          tcp_connections: 0, udp_connections: 0,
        }]
      })
    }
  }, [clock, connected, sample, rateStore])

  return (
    <section className="ui-card tr-panel">
      <header className="tr-panel-head">
        <h3>实时速度</h3>
        <TrafficLegend />
      </header>
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

// ServerTraffic renders the overview metrics and traffic trend charts for one
// node, forwarding the loaded series to the parent so the inbound table can
// display per-port usage for the active time range.
export default function ServerTraffic({
  serverId,
  server,
  reloadKey = 0,
  onDataChange,
}: {
  serverId: number
  server?: Server | null
  reloadKey?: number
  onDataChange?: (data: TrafficSeries | null) => void
}) {
  const screens = Grid.useBreakpoint()
  const compact = !screens.md
  const [data, setData] = useState<TrafficSeries | null>(null)
  const [loading, setLoading] = useState(false)
  const [range, setRange] = useState<TrafficRange>('24h')
  const loadAbortRef = useRef<AbortController | null>(null)
  const loadGenerationRef = useRef(0)
  const onDataChangeRef = useRef(onDataChange)
  onDataChangeRef.current = onDataChange
  const rateStore = useMemo(() => createLiveRateStore(), [])

  const updateData = useCallback((next: TrafficSeries | null) => {
    setData(next)
    onDataChangeRef.current?.(next)
  }, [])

  const load = useCallback(async (quiet = false) => {
    loadAbortRef.current?.abort()
    const controller = new AbortController()
    const generation = ++loadGenerationRef.current
    loadAbortRef.current = controller
    if (!quiet) setLoading(true)
    try {
      const next = await getServerTraffic(serverId, range, controller.signal)
      if (generation === loadGenerationRef.current) updateData(next)
    } catch (error) {
      if (!quiet && !isCanceledRequest(error)) message.error(errMsg(error))
    } finally {
      if (generation === loadGenerationRef.current) {
        loadAbortRef.current = null
        if (!quiet) setLoading(false)
      }
    }
  }, [range, serverId, updateData])

  useEffect(() => {
    updateData(null)
    void load()
    const timer = window.setInterval(() => void load(true), 60000)
    return () => window.clearInterval(timer)
  }, [load, updateData])

  useEffect(() => {
    if (reloadKey > 0) void load(true)
  }, [reloadKey, load])

  useEffect(() => () => {
    loadGenerationRef.current += 1
    loadAbortRef.current?.abort()
  }, [])

  const available = !!data && (!!data.available || !!data.updated_at)
  const total = (value: number) => (available ? formatBytes(value) : '—')
  const memPct = server?.mem_total ? Math.round((server.mem_used / server.mem_total) * 100) : null

  return (
    <div className="tr-overview">
      <Stats
        items={[
          {
            label: '实时速率',
            value: <LiveRateValue store={rateStore} part="download" />,
            hint: <LiveRateValue store={rateStore} part="upload" />,
          },
          {
            label: '区间流量',
            value: total((data?.upload ?? 0) + (data?.download ?? 0)),
            hint: available ? `↓ ${formatBytes(data?.download ?? 0)} · ↑ ${formatBytes(data?.upload ?? 0)}` : undefined,
          },
          { label: '负载', value: server?.online ? server.load1?.toFixed(2) : '—' },
          {
            label: '内存',
            value: server?.online && memPct !== null ? `${memPct}%` : '—',
            hint: server?.online && server.mem_total ? `${formatBytes(server.mem_used)} / ${formatBytes(server.mem_total)}` : undefined,
          },
        ]}
      />

      <div className="tr-grid">
        <LiveChart key={serverId} serverId={serverId} rateStore={rateStore} />
        <section className="ui-card tr-panel">
          <header className="tr-panel-head">
            <h3>
              历史用量
              <span className="tr-panel-note mono">UTC</span>
            </h3>
            <div className="tr-actions">
              {compact ? (
                <Select size="small" aria-label="历史统计范围" style={{ width: 104 }} options={RANGE_OPTIONS} value={range} onChange={setRange} />
              ) : (
                <Segmented<TrafficRange> className="tr-range" aria-label="历史统计范围" options={RANGE_OPTIONS} value={range} onChange={setRange} />
              )}
            </div>
          </header>
          {data && !available ? (
            <div className="tr-chart-empty tr-unavailable">
              <strong>暂未采集到流量</strong>
              <span>重连 Agent 或重新下发配置后开始统计</span>
            </div>
          ) : data ? (
            <TrafficTrend points={data.points} range={range} stepSeconds={data.step_seconds} mode="usage" />
          ) : (
            <div className="tr-chart-empty">{loading ? '读取中…' : '暂无数据'}</div>
          )}
        </section>
      </div>
    </div>
  )
}
