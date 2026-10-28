import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode, UIEvent } from 'react'
import { Button, Input, Select, Switch, message } from '../../../ui'
import {
  Copy,
  Download,
  RotateCw,
  Search,
  ArrowUpToLine,
} from 'lucide-react'
import { errMsg, isCanceledRequest, listServers, serverLogs, streamLogs } from '../../../api'
import type { Server } from '../../../types'
import { parseServerId } from '../../../serverNavigation'
import { useSSE } from '../../../useSSE'
import { useSearchParams } from 'react-router-dom'
import type { SSEMessage } from '../../../useSSE'
import { copyToClipboard } from '../../../util'
import { VirtualList } from '../../../components/VirtualList'
import { PageHeader } from '../../../ui/Page'
import '../../../styles/traffic-logs.css'
import { LogReplay, mergeLogSnapshot } from '../../../logReplay'


const NOISE_PATTERNS = [
  'unknown user password',
  'first record does not look like a TLS handshake',
  'client offered only unsupported versions',
  'no cipher suite supported',
  'fallback disabled',
  "peer doesn't support any of the certificate",
  'connection reset by peer',
]

const MAX_LIVE_LINES = 2000

type LogLevel = '' | 'TRACE' | 'DEBUG' | 'INFO' | 'WARN' | 'ERROR' | 'FATAL'
type LevelFilter = 'all' | 'TRACE' | 'DEBUG' | 'INFO' | 'WARN' | 'ERROR'
type LogDirection = '' | 'inbound' | 'outbound'
type DirectionFilter = 'all' | 'inbound' | 'outbound'
const DIRECTION_OPTIONS: Array<{ value: DirectionFilter; label: string }> = [
  { value: 'all', label: '全部方向' },
  { value: 'inbound', label: '入站' },
  { value: 'outbound', label: '出站' },
]
type DisplayMode = 'compact' | 'raw'

type ParsedLogLine = {
  raw: string
  time: string
  level: LogLevel
  direction: LogDirection
  user: string
  component: string
  context: string
  message: string
  parsed: boolean
}

type LogEntry = ParsedLogLine & { id: number }
const logEntryKey = (line: LogEntry) => line.id

const LEVEL_OPTIONS: Array<{ value: LevelFilter; label: string }> = [
  { value: 'all', label: '全部级别' },
  { value: 'TRACE', label: 'TRACE' },
  { value: 'DEBUG', label: 'DEBUG' },
  { value: 'INFO', label: 'INFO' },
  { value: 'WARN', label: 'WARN' },
  { value: 'ERROR', label: 'ERROR' },
]

function parseLogLine(raw: string): ParsedLogLine {
  let rest = raw.trimEnd()
  let time = ''
  let parsed = false

  // journalctl prefixes sing-box's own timestamp. Keep only one timestamp in
  // compact mode while preserving the complete line in raw mode and copy.
  const journalMatch = rest.match(
    /^[A-Z][a-z]{2}\s+\d{1,2}\s+(\d{2}:\d{2}:\d{2})\s+\S+\s+sing-box\[\d+\]:\s*/,
  )
  if (journalMatch) {
    time = journalMatch[1]
    rest = rest.slice(journalMatch[0].length)
    parsed = true
  }

  const timestampMatch = rest.match(
    /^(?:[+-]\d{4}\s+)?(?:\d{4}-\d{2}-\d{2}\s+)?(\d{2}:\d{2}:\d{2})\s+/,
  )
  if (timestampMatch) {
    time = timestampMatch[1]
    rest = rest.slice(timestampMatch[0].length)
    parsed = true
  }

  let level: LogLevel = ''
  const levelMatch = rest.match(/^(TRACE|DEBUG|INFO|WARN(?:ING)?|ERROR|FATAL)\s+/i)
  if (levelMatch) {
    const value = levelMatch[1].toUpperCase()
    level = value.startsWith('WARN') ? 'WARN' : (value as LogLevel)
    rest = rest.slice(levelMatch[0].length)
    parsed = true
  }

  let context = ''
  const contextMatch = rest.match(/^\[([^\]]+)]\s*/)
  if (contextMatch) {
    context = contextMatch[1]
    rest = rest.slice(contextMatch[0].length)
  }

  let component = ''
  const componentMatch = rest.match(/^([^:\s]+):\s*/)
  if (componentMatch) {
    component = componentMatch[1]
    rest = rest.slice(componentMatch[0].length)
    parsed = true
  }

  let user = ''
  const userMatch = rest.match(/^\[([^\]]+)]\s*/)
  if (userMatch) {
    user = userMatch[1]
    rest = rest.slice(userMatch[0].length)
  }

  const directionSource = `${component} ${rest}`.toLowerCase()
  const direction: LogDirection = directionSource.includes('inbound/')
    ? 'inbound'
    : directionSource.includes('outbound/')
      ? 'outbound'
      : ''

  return {
    raw,
    time,
    level,
    direction,
    user,
    component,
    context,
    message: rest,
    parsed,
  }
}

function matchesLevel(level: LogLevel, filter: LevelFilter) {
  if (filter === 'all') return true
  if (filter === 'ERROR') return level === 'ERROR' || level === 'FATAL'
  return level === filter
}

function levelClass(level: LogLevel) {
  if (level === 'FATAL') return 'error'
  return level ? level.toLowerCase() : 'unknown'
}

function highlightText(value: string, keyword: string): ReactNode {
  const needle = keyword.trim()
  if (!needle) return value

  const lowerValue = value.toLocaleLowerCase()
  const lowerNeedle = needle.toLocaleLowerCase()
  const parts: ReactNode[] = []
  let cursor = 0
  let matchIndex = lowerValue.indexOf(lowerNeedle)

  while (matchIndex !== -1) {
    if (matchIndex > cursor) parts.push(value.slice(cursor, matchIndex))
    parts.push(
      <mark className="lg-highlight" key={`${matchIndex}-${parts.length}`}>
        {value.slice(matchIndex, matchIndex + needle.length)}
      </mark>,
    )
    cursor = matchIndex + needle.length
    matchIndex = lowerValue.indexOf(lowerNeedle, cursor)
  }

  if (cursor < value.length) parts.push(value.slice(cursor))
  return parts.length ? parts : value
}

function renderSemanticText(value: string, keyword: string): ReactNode {
  const tokenPattern = /(\[[^\]\r\n]+])|((?:\d{1,3}\.){3}\d{1,3}(?::\d{1,5})?)|((?:[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?\.)+[a-zA-Z0-9-]{2,}(?::\d{1,5})?)|(\b(?:inbound|outbound|direct|connection|connected|closed|failed|failure|error|timeout|from|to)\b)/gi
  const parts: ReactNode[] = []
  let cursor = 0
  let match = tokenPattern.exec(value)

  while (match) {
    const index = match.index
    const token = match[0]
    if (index > cursor) parts.push(highlightText(value.slice(cursor, index), keyword))

    const lowerToken = token.toLowerCase()
    const tokenClass = token.startsWith('[')
      ? 'lg-token-label'
      : lowerToken === 'failed' || lowerToken === 'failure' || lowerToken === 'error' || lowerToken === 'timeout'
        ? 'lg-token-danger'
        : lowerToken === 'inbound' || lowerToken === 'outbound' || lowerToken === 'direct'
          ? 'lg-token-route'
          : token.includes('.')
            ? 'lg-token-endpoint'
            : 'lg-token-action'

    parts.push(
      <span className={`lg-token ${tokenClass}`} key={`${index}-${parts.length}`}>
        {highlightText(token, keyword)}
      </span>,
    )
    cursor = index + token.length
    match = tokenPattern.exec(value)
  }

  if (cursor < value.length) parts.push(highlightText(value.slice(cursor), keyword))
  return parts.length ? parts : highlightText(value, keyword)
}

function renderContext(value: string, keyword: string): ReactNode {
  if (!value) return ''
  const splitAt = value.lastIndexOf(' ')
  if (splitAt === -1) return <span className="lg-context-id">#{highlightText(value, keyword)}</span>

  return (
    <>
      <span className="lg-context-id">#{highlightText(value.slice(0, splitAt), keyword)}</span>
      <span className="lg-context-separator">·</span>
      <span className="lg-context-duration">{highlightText(value.slice(splitAt + 1), keyword)}</span>
    </>
  )
}

const PROTOCOL_LABELS: Record<string, string> = {
  anytls: 'AnyTLS', snell: 'Snell', shadowsocks: 'SS', ss: 'SS', vless: 'VLESS', vmess: 'VMess', trojan: 'Trojan',
  hysteria2: 'Hy2', hysteria: 'Hysteria', tuic: 'TUIC', socks: 'SOCKS', http: 'HTTP', mixed: 'Mixed',
  direct: 'direct', block: 'block', dns: 'dns', selector: 'select', urltest: 'urltest', wireguard: 'WG',
}

/** Splits "inbound/snell[Hawaii]" into a short protocol label and the user-defined tag. */
function compactComponent(value: string, direction: LogDirection): { proto: string; tag: string } {
  let result = value
  if (direction) {
    const prefix = `${direction}/`
    if (result.toLowerCase().startsWith(prefix)) result = result.slice(prefix.length)
  }
  const match = result.match(/^([^[]+)\[([^\]]+)]$/)
  if (!match) return { proto: '', tag: result }
  const type = match[1].trim().toLowerCase()
  const tag = match[2].trim()
  const proto = PROTOCOL_LABELS[type] ?? match[1].trim()
  // "direct[direct]" or "anytls[anytls]" carry no extra name.
  if (tag.toLowerCase() === type || tag === proto) return { proto: '', tag: proto }
  return { proto, tag }
}

function compactMessage(value: string, direction: LogDirection) {
  if (!direction) return value
  const prefix = `${direction} `
  return value.toLowerCase().startsWith(prefix) ? value.slice(prefix.length) : value
}

function connectionId(context: string) {
  const space = context.lastIndexOf(' ')
  return space === -1 ? context : context.slice(0, space)
}

/** "connection to host:port" → arrow + emphasised target; other text unchanged. */
function renderFlow(message: string, keyword: string): ReactNode {
  const match = message.match(/^connection (to|from) (\S+)(.*)$/i)
  if (!match) return renderSemanticText(message, keyword)
  const [, way, target, rest] = match
  const incoming = way.toLowerCase() === 'from'
  return <>
    <span className="lg-arrow">{incoming ? '← 来自' : '→'}</span>
    <span className={incoming ? 'lg-source' : 'lg-target'}>{highlightText(target, keyword)}</span>
    {rest ? <span className="lg-rest">{renderSemanticText(rest, keyword)}</span> : null}
  </>
}

function formatUpdatedAt(value: number | null) {
  if (!value) return '--:--:--'
  return new Date(value).toLocaleTimeString('zh-CN', { hour12: false })
}

// Logs renders one node's native sing-box log stream. With serverId fixed (server
// detail tab) the node selector is hidden.
export default function Logs({ serverId }: { serverId?: number } = {}) {
  const [servers, setServers] = useState<Server[]>([])
  const [params, setParams] = useSearchParams()
  const requestedId = parseServerId(params.get('server'))
  const sid = serverId ?? (servers.some((server) => server.id === requestedId) ? requestedId : null)
  const lines = 200
  const [logEntries, setLogEntries] = useState<LogEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [live, setLive] = useState(false)
  const [hideNoise, setHideNoise] = useState(true)
  const [keyword, setKeyword] = useState('')
  const [levelFilter, setLevelFilter] = useState<LevelFilter>('all')
  const [directionFilter, setDirectionFilter] = useState<DirectionFilter>('all')
  const displayMode: DisplayMode = 'compact'
  const wrapLines = true
  const [following, setFollowing] = useState(true)
  const [lastUpdatedAt, setLastUpdatedAt] = useState<number | null>(null)
  const boxRef = useRef<HTMLDivElement>(null)
  const followingRef = useRef(true)
  const loadAbortRef = useRef<AbortController | null>(null)
  const loadGenerationRef = useRef(0)
  const nextLogIDRef = useRef(1)
  const pendingLiveLinesRef = useRef<string[]>([])
  const liveSequenceRef = useRef(0)
  const liveEntriesRef = useRef<Array<{ sequence: number; entry: LogEntry }>>([])
  const liveFlushTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const replayRef = useRef<LogReplay | null>(null)
  const streamStartPendingRef = useRef(false)
  const streamActiveRef = useRef(false)

  useEffect(() => {
    if (serverId !== undefined) {
      setLive(false)
      return
    }
    const controller = new AbortController()
    listServers(controller.signal)
      .then((s) => {
        if (controller.signal.aborted) return
        setServers(s)
      })
      .catch((e) => {
        if (!controller.signal.aborted) message.error(errMsg(e))
      })
    return () => {
      controller.abort()
    }
  }, [serverId])

  useEffect(() => {
    if (serverId !== undefined || !servers.length || servers.some((server) => server.id === requestedId)) return
    const first = servers.find((server) => server.online) ?? servers[0]
    const next = new URLSearchParams(params)
    next.set('server', String(first.id))
    setParams(next, { replace: true })
  }, [serverId, servers, requestedId, params, setParams])

  const setFollowState = (value: boolean) => {
    followingRef.current = value
    setFollowing(value)
  }

  const scrollToLatest = useCallback((smooth = false) => {
    followingRef.current = true
    setFollowing(true)
    requestAnimationFrame(() => {
      const box = boxRef.current
      if (box) box.scrollTo({ top: 0, behavior: smooth ? 'smooth' : 'auto' })
    })
  }, [])

  const load = useCallback(async (quiet = false) => {
    if (!sid) return
    loadAbortRef.current?.abort()
    const controller = new AbortController()
    const generation = ++loadGenerationRef.current
    const liveSequenceAtStart = liveSequenceRef.current
    loadAbortRef.current = controller
    if (!quiet) setLoading(true)
    try {
      const t = await serverLogs(sid, lines, controller.signal)
      if (generation !== loadGenerationRef.current) return
      const entries = t
        .split('\n')
        .filter((raw) => raw.trim().length > 0)
        .map((raw) => ({ ...parseLogLine(raw), id: nextLogIDRef.current++ }))
      const arrivedDuringLoad = liveEntriesRef.current
        .filter((item) => item.sequence > liveSequenceAtStart)
        .map((item) => item.entry)
      const merged = mergeLogSnapshot(entries.map((entry) => entry.raw), arrivedDuringLoad.map((entry) => entry.raw))
        .map((raw) => ({ ...parseLogLine(raw), id: nextLogIDRef.current++ }))
      if (!streamActiveRef.current) replayRef.current = new LogReplay(entries.map((entry) => entry.raw))
      setLogEntries(merged.length > MAX_LIVE_LINES ? merged.slice(merged.length - MAX_LIVE_LINES) : merged)
      setLastUpdatedAt(Date.now())
      scrollToLatest()
      // Live tail is the default view; it only pauses when the stream fails
      // or the node goes offline.
      if (servers.find((s) => s.id === sid)?.online !== false) setLive(true)
    } catch (e) {
      if (!quiet && !isCanceledRequest(e)) message.error(errMsg(e))
    } finally {
      if (generation === loadGenerationRef.current) {
        loadAbortRef.current = null
        if (!quiet) setLoading(false)
      }
    }
  }, [lines, scrollToLatest, servers, sid])

  useEffect(() => () => {
    loadGenerationRef.current += 1
    loadAbortRef.current?.abort()
    if (liveFlushTimerRef.current) clearTimeout(liveFlushTimerRef.current)
    liveFlushTimerRef.current = null
    pendingLiveLinesRef.current = []
    liveEntriesRef.current = []
    liveSequenceRef.current = 0
    replayRef.current = null
  }, [])

  // SSE live log streaming: connect to the same SSE endpoint and filter for
  // kind === "log" messages. The agent-side native log follower is started/stopped
  // via the streamLogs API call below.
  const liveUrl = sid ? `/api/admin/servers/${sid}/traffic/live` : ''
  const flushLiveLines = useCallback(() => {
    if (streamStartPendingRef.current) return
    if (liveFlushTimerRef.current) return
    // Batch bursts from the native log follower so parsing/filtering/rendering happens at
    // most ten times per second instead of once per log line.
    liveFlushTimerRef.current = setTimeout(() => {
      liveFlushTimerRef.current = null
      const pending = pendingLiveLinesRef.current.splice(0)
      if (!pending.length) return
      const fresh = replayRef.current?.consume(pending) ?? pending
      const additions = fresh.map((raw) => ({ ...parseLogLine(raw), id: nextLogIDRef.current++ }))
      if (!additions.length) return
      for (const entry of additions) {
        liveEntriesRef.current.push({ sequence: ++liveSequenceRef.current, entry })
      }
      if (liveEntriesRef.current.length > MAX_LIVE_LINES) {
        liveEntriesRef.current.splice(0, liveEntriesRef.current.length - MAX_LIVE_LINES)
      }
      // New lines are inserted above the reader; keep their place unless following.
      const box = boxRef.current
      const heightBefore = box?.scrollHeight ?? 0
      setLogEntries((current) => {
        const next = [...current, ...additions]
        return next.length > MAX_LIVE_LINES ? next.slice(next.length - MAX_LIVE_LINES) : next
      })
      setLastUpdatedAt(Date.now())
      requestAnimationFrame(() => {
        const viewer = boxRef.current
        if (!viewer) return
        if (followingRef.current) viewer.scrollTop = 0
        else viewer.scrollTop += Math.max(0, viewer.scrollHeight - heightBefore)
      })
    }, 100)
  }, [])
  const onSSEMessage = useCallback((msg: SSEMessage) => {
    if (msg.kind !== 'log' || !msg.data) return
    const logEvt = msg.data as { level: string; msg: string }
    pendingLiveLinesRef.current.push(...logEvt.msg.split('\n').filter((raw) => raw.trim().length > 0))
    flushLiveLines()
  }, [flushLiveLines])
  const { connected: sseConnected } = useSSE(liveUrl, live && !!sid, onSSEMessage)

  // Start one exact server's native log stream and always stop that same
  // server on toggle, server switch, or page unmount. If the start request
  // finishes after cleanup, send a second stop so request reordering cannot
  // leave an orphan stream running on the agent.
  useEffect(() => {
    if (!sid || !live) return
    const streamServerID = sid
    const streamLines = lines
    // Every effect lifetime owns a distinct backend reference. Reusing one ID
    // lets a delayed cleanup from an earlier StrictMode/effect generation stop
    // the newly-started stream for the same browser tab.
    const sessionID = globalThis.crypto?.randomUUID?.()
      ?? `lg-${Date.now()}-${Math.random().toString(36).slice(2)}`
    let disposed = false
    let renewTimer: ReturnType<typeof setInterval> | null = null
    streamStartPendingRef.current = true

    void streamLogs(streamServerID, { enable: true, lines: streamLines, session_id: sessionID })
      .then((response) => {
        if (disposed) {
          void streamLogs(streamServerID, { enable: false, session_id: sessionID }).catch(() => {})
          return
        }
        if (response.output === 'joined existing log stream') replayRef.current = null
        streamStartPendingRef.current = false
        streamActiveRef.current = true
        flushLiveLines()
        renewTimer = setInterval(() => {
          void streamLogs(streamServerID, { enable: true, lines: streamLines, session_id: sessionID })
            .catch((error) => {
              if (disposed) return
              message.error(`实时日志租约续期失败：${errMsg(error)}`)
              setLive(false)
            })
        }, 45_000)
      })
      .catch((e) => {
        if (disposed) return
        streamStartPendingRef.current = false
        pendingLiveLinesRef.current = []
        message.error(errMsg(e))
        setLive(false)
      })

    return () => {
      disposed = true
      streamStartPendingRef.current = false
      streamActiveRef.current = false
      if (renewTimer) clearInterval(renewTimer)
      void streamLogs(streamServerID, { enable: false, session_id: sessionID }).catch(() => {})
    }
    // The line count only takes effect when live mode is enabled. Changing it
    // is handled by the reload effect below, which first turns live mode off.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [live, sid, flushLiveLines])

  // When switching servers or line count, stop live and reload.
  useEffect(() => {
    setLive(false)
    pendingLiveLinesRef.current = []
    liveEntriesRef.current = []
    liveSequenceRef.current = 0
    replayRef.current = null
    if (liveFlushTimerRef.current) clearTimeout(liveFlushTimerRef.current)
    liveFlushTimerRef.current = null
    setLogEntries([])
    setLastUpdatedAt(null)
    setLoading(false)
    void load()
    return () => {
      loadGenerationRef.current += 1
      loadAbortRef.current?.abort()
    }
  }, [load])

  const filteredLines = useMemo(() => {
    const kw = keyword.trim().toLocaleLowerCase()
    return logEntries.filter((line) => {
      const lowerRaw = line.raw.toLocaleLowerCase()
      if (hideNoise && NOISE_PATTERNS.some((pattern) => lowerRaw.includes(pattern))) return false
      if (!matchesLevel(line.level, levelFilter)) return false
      if (directionFilter !== 'all' && line.direction !== directionFilter) return false
      return !kw || lowerRaw.includes(kw)
    })
  }, [directionFilter, hideNoise, keyword, levelFilter, logEntries])

  // Consecutive lines of one connection (inbound + outbound) share a band so
  // pairs read together; the band flips whenever the connection id changes.
  const connectionBands = useMemo(() => {
    const bands = new Map<LogEntry["id"], boolean>()
    let band = false
    let previous = ''
    for (const line of filteredLines) {
      const id = connectionId(line.context)
      if (!id || id !== previous) band = !band
      previous = id
      bands.set(line.id, band)
    }
    return bands
  }, [filteredLines])

  // Newest first: the live edge sits at the top of the viewer.
  const displayLines = useMemo(() => [...filteredLines].reverse(), [filteredLines])

  const previousSid = useRef(sid)
  useEffect(() => {
    if (previousSid.current !== sid) { previousSid.current = sid; setLive(false) }
  }, [sid])

  const current = servers.find((s) => s.id === sid)

  const handleScroll = (event: UIEvent<HTMLDivElement>) => {
    const box = event.currentTarget
    const isAtTop = box.scrollTop < 36
    if (isAtTop !== followingRef.current) setFollowState(isAtTop)
  }

  const handleLiveChange = (checked: boolean) => {
    if (checked && !live) replayRef.current = new LogReplay(logEntries.slice(-lines).map((entry) => entry.raw))
    setLive(checked)
    if (checked) scrollToLatest()
  }

  const copyLine = async (raw: string) => {
    try {
      await copyToClipboard(raw)
      message.success('该行日志已复制')
    } catch {
      message.error('复制失败，请手动选择日志文本')
    }
  }

  const downloadLogs = () => {
    if (!filteredLines.length) return
    const blob = new Blob([filteredLines.map((line) => line.raw).join('\n')], {
      type: 'text/plain;charset=utf-8',
    })
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    const timestamp = new Date().toISOString().replace(/[:.]/g, '-')
    anchor.href = url
    anchor.download = `sing-box-logs-${sid ?? 'server'}-${timestamp}.log`
    anchor.click()
    window.setTimeout(() => URL.revokeObjectURL(url), 1000)
  }

  const liveStatus = !live ? '已暂停' : sseConnected ? '实时' : '连接中'
  const liveTone = !live ? 'is-paused' : sseConnected ? 'is-live' : 'is-connecting'
  const emptyDescription = loading
    ? '读取中…'
      : logEntries.length
      ? '没有匹配的日志'
      : '暂无日志'

  return (
    <div className="lg-page">
      <PageHeader
        title="日志"
        actions={
          <Button icon={<RotateCw />} loading={loading} onClick={() => load()}>刷新</Button>
        }
      />

      <div className="lg-toolbar">
        <Select
          className="lg-filter-select"
          value={levelFilter}
          onChange={(value: LevelFilter) => setLevelFilter(value)}
          options={LEVEL_OPTIONS}
          aria-label="日志级别"
        />
        <Select
          className="lg-filter-select"
          value={directionFilter}
          onChange={(value: DirectionFilter) => setDirectionFilter(value)}
          options={DIRECTION_OPTIONS}
          aria-label="方向"
        />
        <Input
          className="lg-search"
          prefix={<Search />}
          placeholder="搜索"
          aria-label="搜索日志"
          value={keyword}
          onChange={(event) => setKeyword(event.target.value)}
          allowClear
        />
        <label className="lg-toggle">
          <Switch size="small" checked={hideNoise} onChange={setHideNoise} />
          <span>过滤噪音</span>
        </label>
      </div>

      <div className="lg-box">
        <div className="lg-statusbar">
          <div className="lg-status-group">
            <span className={`lg-live ${liveTone}`}><i />{liveStatus}</span>
            {keyword.trim() ? <span className="lg-match mono">{filteredLines.length} 条匹配</span> : null}
            {current && !current.online ? <span className="lg-offline">节点离线</span> : null}
          </div>
          <div className="lg-status-group lg-status-right">
            <span className="lg-count mono" title={`最后更新 ${formatUpdatedAt(lastUpdatedAt)}`}>
              {filteredLines.length} / {logEntries.length} 行 · {formatUpdatedAt(lastUpdatedAt)}
            </span>
            {!live && current?.online ? (
              <Button size="small" type="text" onClick={() => handleLiveChange(true)}>恢复实时</Button>
            ) : null}
            <Button
              size="small"
              type="text"
              icon={<Download />}
              disabled={!filteredLines.length}
              onClick={downloadLogs}
              aria-label="下载日志"
            >
              下载
            </Button>
          </div>
        </div>

        {filteredLines.length ? (
          <div className="lg-viewer-shell">
            <VirtualList
              ref={boxRef}
              items={displayLines}
              getKey={logEntryKey}
              estimatedItemHeight={28}
              overscan={12}
              className={`lg-viewer ${wrapLines ? 'is-wrap' : 'is-nowrap'}`}
              onScroll={handleScroll}
              role="log"
              ariaLabel="sing-box 日志内容"
              renderItem={(line) => {
                const compact = displayMode === 'compact' && line.parsed
                const componentText = compactComponent(line.component || 'sing-box', line.direction)
                const messageText = compactMessage(line.message, line.direction)
                return (
                  <div
                    className={`lg-line ${compact ? 'lg-line-compact' : 'lg-line-raw'} lg-line-${levelClass(line.level)}${connectionBands.get(line.id) ? ' is-band' : ''}`}
                    title={compact ? line.raw : undefined}
                  >
                    {compact ? (
                      <>
                        <span className="lg-time">{line.time || '--:--:--'}</span>
                        <span className={`lg-dir lg-dir-${line.direction || 'none'}`}>
                          {line.direction === 'inbound' ? '入' : line.direction === 'outbound' ? '出' : '·'}
                        </span>
                        <span className="lg-subject">
                          {['warn', 'error'].includes(levelClass(line.level)) ? <span className={`lg-badge lg-badge-${levelClass(line.level)}`}>{line.level}</span> : null}
                          {componentText.proto ? <span className="lg-proto">{componentText.proto}</span> : null}
                          <span className={`lg-component lg-component-${line.direction || 'neutral'}`} title={line.component}>{highlightText(componentText.tag, keyword)}</span>
                          {line.user ? <span className="lg-user" title={`用户 ${line.user}`}>{highlightText(line.user, keyword)}</span> : null}
                        </span>
                        <span className="lg-message">{renderFlow(messageText, keyword)}</span>
                        <span className="lg-context" title={line.context}>
                          {renderContext(line.context, keyword)}
                        </span>
                      </>
                    ) : (
                      <span className="lg-raw-text">{renderSemanticText(line.raw, keyword)}</span>
                    )}
                    <button
                      type="button"
                      className="lg-copy-button"
                      onClick={() => copyLine(line.raw)}
                      title="复制该行"
                      aria-label="复制该行日志"
                    >
                      <Copy />
                    </button>
                  </div>
                )
              }}
            />

            {!following ? (
              <Button
                className="lg-jump-latest"
                type="primary"
                size="small"
                icon={<ArrowUpToLine />}
                onClick={() => scrollToLatest(true)}
              >
                回到最新
              </Button>
            ) : null}
          </div>
        ) : (
          <div className="lg-empty">{emptyDescription}</div>
        )}
      </div>
    </div>
  )
}
