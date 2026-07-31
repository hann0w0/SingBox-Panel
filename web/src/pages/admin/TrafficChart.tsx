import { useId, useMemo, useRef, useState } from 'react'
import { Grid } from 'antd'
import type { TrafficPoint, TrafficRange } from '../../types'

export type TrafficSeriesKey = 'upload' | 'download'

export interface TrafficChartSeries {
  key: TrafficSeriesKey
  label: string
  color: string
}

interface TrafficChartProps {
  points: TrafficPoint[]
  range: TrafficRange
  series: TrafficChartSeries[]
  valueMode?: 'bytes' | 'rate'
  stepSeconds?: number
  ariaLabel?: string
}

const GRID_LINES = 4

interface ChartGeometry {
  width: number
  height: number
  padding: { top: number; right: number; bottom: number; left: number }
  plotWidth: number
  plotHeight: number
  maxWidth: number
  tickCount: number
  fontSize: number
  pointRadius: number
}

function chartGeometry(compact: boolean): ChartGeometry {
  const width = compact ? 360 : 960
  const height = compact ? 220 : 248
  const padding = compact
    ? { top: 10, right: 10, bottom: 42, left: 54 }
    : { top: 10, right: 16, bottom: 52, left: 68 }
  return {
    width,
    height,
    padding,
    plotWidth: width - padding.left - padding.right,
    plotHeight: height - padding.top - padding.bottom,
    maxWidth: width,
    tickCount: compact ? 5 : 10,
    fontSize: compact ? 10 : 11,
    pointRadius: compact ? 2.2 : 2.5,
  }
}

export function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const unit = Math.max(0, Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1))
  const scaled = value / 1024 ** unit
  const digits = scaled >= 100 || unit === 0 ? 0 : scaled >= 10 ? 1 : 2
  return `${scaled.toFixed(digits)} ${units[unit]}`
}

function formatRate(value: number): string {
  return `${formatBytes(value)}/s`
}

function formatTime(value: string, range: TrafficRange, compact: boolean): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  const day = `${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
  if (range === '30d') return day
  if (range === '7d') {
    if (compact) return day
    const time = date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false })
    return `${day} ${time}`
  }
  return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false })
}

function linePath(values: number[], maxValue: number, geometry: ChartGeometry): string {
  if (!values.length) return ''
  const { padding, plotHeight, plotWidth } = geometry
  return values
    .map((value, index) => {
      const x = padding.left + (values.length === 1 ? plotWidth / 2 : (index / (values.length - 1)) * plotWidth)
      const y = padding.top + plotHeight - (value / maxValue) * plotHeight
      return `${index === 0 ? 'M' : 'L'} ${x.toFixed(2)} ${y.toFixed(2)}`
    })
    .join(' ')
}

function pointValue(point: TrafficPoint, key: TrafficSeriesKey, valueMode: 'bytes' | 'rate', stepSeconds: number): number {
  const value = point[key]
  return valueMode === 'rate' ? value / Math.max(stepSeconds, 1) : value
}

export default function TrafficChart({
  points,
  range,
  series,
  valueMode = 'bytes',
  stepSeconds = 1,
  ariaLabel = '节点流量趋势图',
}: TrafficChartProps) {
  const screens = Grid.useBreakpoint()
  const compact = !screens.sm
  const geometry = useMemo(() => chartGeometry(compact), [compact])
  const { width, height, padding, plotWidth, plotHeight } = geometry
  const svgRef = useRef<SVGSVGElement>(null)
  const [activeIndex, setActiveIndex] = useState<number | null>(null)
  const gradientPrefix = useId().replace(/:/g, '')
  const formatValue = valueMode === 'rate' ? formatRate : formatBytes
  const values = useMemo(
    () => series.map((item) => points.map((point) => pointValue(point, item.key, valueMode, stepSeconds))),
    [points, series, stepSeconds, valueMode],
  )
  const maxValue = useMemo(() => {
    const raw = Math.max(0, ...values.flat())
    if (raw === 0) return 1
    const magnitude = 10 ** Math.floor(Math.log10(raw))
    return Math.ceil(raw / magnitude) * magnitude
  }, [values])
  const paths = useMemo(() => values.map((item) => linePath(item, maxValue, geometry)), [geometry, maxValue, values])

  const tickIndexes = useMemo(() => {
    if (!points.length) return []
    const count = Math.min(geometry.tickCount, points.length)
    if (count === 1) return [0]
    return Array.from(new Set(Array.from({ length: count }, (_, i) => Math.round((i / (count - 1)) * (points.length - 1)))))
  }, [geometry.tickCount, points])

  const activePoint = activeIndex === null ? null : points[activeIndex]
  const activeX = activeIndex === null
    ? 0
    : padding.left + (points.length === 1 ? plotWidth / 2 : (activeIndex / (points.length - 1)) * plotWidth)

  const handlePointer = (clientX: number) => {
    if (!svgRef.current || !points.length) return
    const rect = svgRef.current.getBoundingClientRect()
    const svgX = ((clientX - rect.left) / rect.width) * width
    const ratio = Math.max(0, Math.min(1, (svgX - padding.left) / plotWidth))
    setActiveIndex(Math.round(ratio * (points.length - 1)))
  }

  return (
    <div style={{ position: 'relative', width: '100%', maxWidth: geometry.maxWidth, margin: '0 auto' }}>
      <div style={{ display: 'flex', justifyContent: 'flex-end', flexWrap: 'wrap', gap: compact ? 12 : 18, minHeight: 20, color: '#596273', fontSize: compact ? 11 : 13 }}>
        {series.map((item) => (
          <span key={item.key}>
            <i style={{ display: 'inline-block', width: compact ? 16 : 22, height: 7, marginRight: 6, verticalAlign: 'middle', background: item.color, opacity: 0.68 }} />
            {item.label}
          </span>
        ))}
      </div>
      <svg
        ref={svgRef}
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label={ariaLabel}
        style={{ display: 'block', width: '100%', height: 'auto', touchAction: 'none' }}
        onMouseMove={(event) => handlePointer(event.clientX)}
        onMouseLeave={() => setActiveIndex(null)}
        onTouchMove={(event) => handlePointer(event.touches[0].clientX)}
        onTouchEnd={() => setActiveIndex(null)}
      >
        <defs>
          {series.map((item) => (
            <linearGradient key={item.key} id={`${gradientPrefix}-${item.key}`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0" stopColor={item.color} stopOpacity="0.38" />
              <stop offset="1" stopColor={item.color} stopOpacity="0.04" />
            </linearGradient>
          ))}
        </defs>

        {Array.from({ length: GRID_LINES + 1 }, (_, index) => {
          const y = padding.top + (index / GRID_LINES) * plotHeight
          const value = maxValue * (1 - index / GRID_LINES)
          return (
            <g key={index}>
              <line x1={padding.left} y1={y} x2={width - padding.right} y2={y} stroke="#e8ebf0" strokeWidth="1" />
              <text x={padding.left - 8} y={y + 3} textAnchor="end" fill="#7d8590" fontSize={geometry.fontSize}>
                {formatValue(value)}
              </text>
            </g>
          )
        })}

        <line x1={padding.left} y1={padding.top} x2={padding.left} y2={height - padding.bottom} stroke="#b8c0cc" />
        <line x1={padding.left} y1={height - padding.bottom} x2={width - padding.right} y2={height - padding.bottom} stroke="#b8c0cc" />

        {tickIndexes.map((index) => {
          const x = padding.left + (points.length === 1 ? plotWidth / 2 : (index / (points.length - 1)) * plotWidth)
          const labelX = index === 0 ? x + (compact ? 16 : 12) : x
          const labelY = height - padding.bottom + (compact ? 14 : 16)
          return (
            <g key={index}>
              <line x1={x} y1={height - padding.bottom} x2={x} y2={height - padding.bottom + 4} stroke="#b8c0cc" />
              <text
                x={labelX}
                y={labelY}
                textAnchor="end"
                fill="#7d8590"
                fontSize={geometry.fontSize}
                transform={`rotate(-38 ${labelX} ${labelY})`}
              >
                {formatTime(points[index].time, range, compact)}
              </text>
            </g>
          )
        })}

        {paths.map((path, index) => path ? (
          <g key={series[index].key}>
            <path
              d={`${path} L ${width - padding.right} ${height - padding.bottom} L ${padding.left} ${height - padding.bottom} Z`}
              fill={`url(#${gradientPrefix}-${series[index].key})`}
            />
            <path d={path} fill="none" stroke={series[index].color} strokeWidth="2.2" strokeLinejoin="round" strokeLinecap="round" />
            {values[index].map((value, pointIndex) => {
              const x = padding.left + (values[index].length === 1 ? plotWidth / 2 : (pointIndex / (values[index].length - 1)) * plotWidth)
              const y = padding.top + plotHeight - (value / maxValue) * plotHeight
              return <circle key={pointIndex} cx={x} cy={y} r={geometry.pointRadius} fill="#fff" stroke={series[index].color} strokeWidth="1.5" />
            })}
          </g>
        ) : null)}

        {activePoint ? (
          <g pointerEvents="none">
            <line x1={activeX} y1={padding.top} x2={activeX} y2={height - padding.bottom} stroke="#8c96a3" strokeDasharray="4 4" />
            {series.map((item) => {
              const value = pointValue(activePoint, item.key, valueMode, stepSeconds)
              return (
              <circle
                key={item.key}
                cx={activeX}
                cy={padding.top + plotHeight - (value / maxValue) * plotHeight}
                r={compact ? 4 : 5}
                fill="#fff"
                stroke={item.color}
                strokeWidth="3"
              />
              )
            })}
          </g>
        ) : null}
      </svg>

      {activePoint ? (
        <div
          style={{
            position: 'absolute',
            top: compact ? 18 : 16,
            left: `${Math.max(8, Math.min(78, (activeX / width) * 100))}%`,
            transform: activeX > width * 0.68 ? 'translateX(-100%)' : 'translateX(8px)',
            zIndex: 1,
            minWidth: compact ? 132 : 156,
            padding: compact ? '7px 9px' : '9px 11px',
            border: '1px solid #e5e8ed',
            borderRadius: 6,
            background: 'rgba(255,255,255,0.96)',
            boxShadow: '0 6px 20px rgba(31,42,55,0.12)',
            pointerEvents: 'none',
            fontSize: compact ? 11 : 12,
          }}
        >
          <div style={{ color: '#596273', marginBottom: 6 }}>{new Date(activePoint.time).toLocaleString('zh-CN', { hour12: false })}</div>
          {series.map((item, index) => (
            <div key={item.key} style={{ display: 'flex', justifyContent: 'space-between', gap: 20, marginBottom: index === series.length - 1 ? 0 : 4 }}>
              <span style={{ color: item.color }}>{item.label}</span>
              <strong>{formatValue(pointValue(activePoint, item.key, valueMode, stepSeconds))}</strong>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  )
}
