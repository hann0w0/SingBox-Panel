import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode, UIEvent } from 'react'
import { Button, Card, Empty, Input, Select, Switch, Tag, message } from 'antd'
import {
  CopyOutlined,
  DownloadOutlined,
  ReloadOutlined,
  SearchOutlined,
  VerticalAlignBottomOutlined,
} from '@ant-design/icons'
import { errMsg, isCanceledRequest, listServers, serverLogs, streamLogs } from '../../../api'
import type { Server } from '../../../types'
import { useSSE } from '../../../useSSE'
import type { SSEMessage } from '../../../useSSE'
import { copyToClipboard } from '../../../util'
import { VirtualList } from '../../../components/VirtualList'

const LINE_OPTIONS = [100, 200, 500, 1000]

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

const DIRECTION_OPTIONS: Array<{ value: DirectionFilter; label: string }> = [
  { value: 'all', label: '全部方向' },
  { value: 'inbound', label: '入站' },
  { value: 'outbound', label: '出站' },
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
      <mark className="logs-highlight" key={`${matchIndex}-${parts.length}`}>
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
      ? 'logs-token-label'
      : lowerToken === 'failed' || lowerToken === 'failure' || lowerToken === 'error' || lowerToken === 'timeout'
        ? 'logs-token-danger'
        : lowerToken === 'inbound' || lowerToken === 'outbound' || lowerToken === 'direct'
          ? 'logs-token-route'
          : token.includes('.')
            ? 'logs-token-endpoint'
            : 'logs-token-action'

    parts.push(
      <span className={`logs-token ${tokenClass}`} key={`${index}-${parts.length}`}>
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
  if (splitAt === -1) return <span className="logs-context-id">#{highlightText(value, keyword)}</span>

  return (
    <>
      <span className="logs-context-id">#{highlightText(value.slice(0, splitAt), keyword)}</span>
      <span className="logs-context-separator">·</span>
      <span className="logs-context-duration">{highlightText(value.slice(splitAt + 1), keyword)}</span>
    </>
  )
}

function compactComponent(value: string, direction: LogDirection) {
  let result = value
  if (direction) {
    const prefix = `${direction}/`
    if (result.toLowerCase().startsWith(prefix)) result = result.slice(prefix.length)
  }

  const duplicatedLabel = result.match(/^([^[]+)\[([^\]]+)]$/)
  if (duplicatedLabel && duplicatedLabel[1].toLowerCase() === duplicatedLabel[2].toLowerCase()) {
    return duplicatedLabel[2]
  }
  return result
}

function compactMessage(value: string, direction: LogDirection) {
  if (!direction) return value
  const prefix = `${direction} `
  return value.toLowerCase().startsWith(prefix) ? value.slice(prefix.length) : value
}

function formatUpdatedAt(value: number | null) {
  if (!value) return '尚未更新'
  return new Date(value).toLocaleTimeString('zh-CN', { hour12: false })
}

export default function Logs() {
  const [servers, setServers] = useState<Server[]>([])
  const [sid, setSid] = useState<number | null>(null)
  const [lines, setLines] = useState(200)
  const [logEntries, setLogEntries] = useState<LogEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [live, setLive] = useState(false)
  const [hideNoise, setHideNoise] = useState(true)
  const [keyword, setKeyword] = useState('')
  const [levelFilter, setLevelFilter] = useState<LevelFilter>('all')
  const [directionFilter, setDirectionFilter] = useState<DirectionFilter>('all')
  const [displayMode, setDisplayMode] = useState<DisplayMode>('compact')
  const [wrapLines, setWrapLines] = useState(true)
  const [following, setFollowing] = useState(true)
  const [lastUpdatedAt, setLastUpdatedAt] = useState<number | null>(null)
  const boxRef = useRef<HTMLDivElement>(null)
  const followingRef = useRef(true)
  const loadAbortRef = useRef<AbortController | null>(null)
  const loadGenerationRef = useRef(0)
  const serverListAbortRef = useRef<AbortController | null>(null)
  const nextLogIDRef = useRef(1)
  const pendingLiveLinesRef = useRef<string[]>([])
  const liveFlushTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    serverListAbortRef.current = controller
    listServers(controller.signal)
      .then((s) => {
        if (controller.signal.aborted) return
        setServers(s)
        const first = s.find((x) => x.online) ?? s[0]
        if (first) setSid(first.id)
      })
      .catch((e) => {
        if (!controller.signal.aborted) message.error(errMsg(e))
      })
    return () => {
      controller.abort()
      if (serverListAbortRef.current === controller) serverListAbortRef.current = null
    }
  }, [])

  const setFollowState = (value: boolean) => {
    followingRef.current = value
    setFollowing(value)
  }

  const scrollToLatest = useCallback((smooth = false) => {
    followingRef.current = true
    setFollowing(true)
    requestAnimationFrame(() => {
      const box = boxRef.current
      if (box) box.scrollTo({ top: box.scrollHeight, behavior: smooth ? 'smooth' : 'auto' })
    })
  }, [])

  const load = useCallback(async (quiet = false) => {
    if (!sid) return
    loadAbortRef.current?.abort()
    const controller = new AbortController()
    const generation = ++loadGenerationRef.current
    loadAbortRef.current = controller
    if (!quiet) setLoading(true)
    try {
      const t = await serverLogs(sid, lines, controller.signal)
      if (generation !== loadGenerationRef.current) return
      const entries = t
        .split('\n')
        .filter((raw) => raw.trim().length > 0)
        .map((raw) => ({ ...parseLogLine(raw), id: nextLogIDRef.current++ }))
      setLogEntries(entries)
      setLastUpdatedAt(Date.now())
      scrollToLatest()
    } catch (e) {
      if (!quiet && !isCanceledRequest(e)) message.error(errMsg(e))
    } finally {
      if (generation === loadGenerationRef.current) {
        loadAbortRef.current = null
        if (!quiet) setLoading(false)
      }
    }
  }, [lines, scrollToLatest, sid])

  useEffect(() => () => {
    loadGenerationRef.current += 1
    loadAbortRef.current?.abort()
    if (liveFlushTimerRef.current) clearTimeout(liveFlushTimerRef.current)
    liveFlushTimerRef.current = null
    pendingLiveLinesRef.current = []
  }, [])

  // SSE live log streaming: connect to the same SSE endpoint and filter for
  // kind === "log" messages. The agent-side journalctl -f is started/stopped
  // via the streamLogs API call below.
  const liveUrl = sid ? `/api/admin/servers/${sid}/traffic/live` : ''
  const onSSEMessage = useCallback((msg: SSEMessage) => {
    if (msg.kind !== 'log' || !msg.data) return
    const logEvt = msg.data as { level: string; msg: string }
    pendingLiveLinesRef.current.push(...logEvt.msg.split('\n').filter((raw) => raw.trim().length > 0))
    if (liveFlushTimerRef.current) return
    // Batch bursts from journalctl so parsing/filtering/rendering happens at
    // most ten times per second instead of once per log line.
    liveFlushTimerRef.current = setTimeout(() => {
      liveFlushTimerRef.current = null
      const pending = pendingLiveLinesRef.current.splice(0)
      if (!pending.length) return
      const additions = pending.map((raw) => ({ ...parseLogLine(raw), id: nextLogIDRef.current++ }))
      setLogEntries((current) => {
        const next = [...current, ...additions]
        return next.length > MAX_LIVE_LINES ? next.slice(next.length - MAX_LIVE_LINES) : next
      })
      setLastUpdatedAt(Date.now())
      if (followingRef.current) {
        requestAnimationFrame(() => {
          const box = boxRef.current
          if (box) box.scrollTop = box.scrollHeight
        })
      }
    }, 100)
  }, [])
  const { connected: sseConnected } = useSSE(liveUrl, live && !!sid, onSSEMessage)

  // Start one exact server's journalctl stream and always stop that same
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
      ?? `logs-${Date.now()}-${Math.random().toString(36).slice(2)}`
    let disposed = false
    let renewTimer: ReturnType<typeof setInterval> | null = null

    void streamLogs(streamServerID, { enable: true, lines: streamLines, session_id: sessionID })
      .then(() => {
        if (disposed) {
          void streamLogs(streamServerID, { enable: false, session_id: sessionID }).catch(() => {})
          return
        }
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
        message.error(errMsg(e))
        setLive(false)
      })

    return () => {
      disposed = true
      if (renewTimer) clearInterval(renewTimer)
      void streamLogs(streamServerID, { enable: false, session_id: sessionID }).catch(() => {})
    }
    // The line count only takes effect when live mode is enabled. Changing it
    // is handled by the reload effect below, which first turns live mode off.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [live, sid])

  // When switching servers or line count, stop live and reload.
  useEffect(() => {
    setLive(false)
    pendingLiveLinesRef.current = []
    if (liveFlushTimerRef.current) clearTimeout(liveFlushTimerRef.current)
    liveFlushTimerRef.current = null
    setLogEntries([])
    void load()
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

  const current = servers.find((s) => s.id === sid)

  const handleScroll = (event: UIEvent<HTMLDivElement>) => {
    const box = event.currentTarget
    const isAtBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 36
    if (isAtBottom !== followingRef.current) setFollowState(isAtBottom)
  }

  const handleLiveChange = (checked: boolean) => {
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

  const liveStatus = !live ? '实时关闭' : sseConnected ? '实时已连接' : '实时连接中'
  const emptyDescription = loading
    ? '读取中…'
      : logEntries.length
      ? '没有符合当前筛选条件的日志'
      : '暂无日志'

  return (
    <Card
      className="logs-card"
      title="sing-box 日志"
      extra={
        <Button icon={<ReloadOutlined />} loading={loading} onClick={() => load()} title="刷新" aria-label="刷新" />
      }
    >
      <div className="logs-toolbar">
        <Select
          className="logs-server-select"
          value={sid ?? undefined}
          onChange={(next) => {
            setLive(false)
            setSid(next)
          }}
          placeholder="选择节点"
          popupMatchSelectWidth={false}
          options={servers.map((s) => ({
            value: s.id,
            label: `${s.name}${s.region ? ' · ' + s.region : ''}${s.online ? '' : '（离线）'}`,
            disabled: !s.online,
          }))}
        />
        <Select
          className="logs-lines-select"
          value={lines}
          onChange={setLines}
          options={LINE_OPTIONS.map((n) => ({ value: n, label: `最近 ${n} 行` }))}
        />
        <Select
          className="logs-filter-select"
          value={levelFilter}
          onChange={(value: LevelFilter) => setLevelFilter(value)}
          options={LEVEL_OPTIONS}
        />
        <Select
          className="logs-filter-select"
          value={directionFilter}
          onChange={(value: DirectionFilter) => setDirectionFilter(value)}
          options={DIRECTION_OPTIONS}
        />
        <Input
          className="logs-search"
          prefix={<SearchOutlined />}
          placeholder="搜索日志"
          value={keyword}
          onChange={(event) => setKeyword(event.target.value)}
          allowClear
        />
        <label className="logs-toggle">
          <Switch size="small" checked={hideNoise} onChange={setHideNoise} />
          <span>过滤噪音</span>
        </label>
        <label className="logs-toggle">
          <Switch
            size="small"
            checked={live}
            onChange={handleLiveChange}
            disabled={!current?.online}
          />
          <span>实时</span>
        </label>
      </div>

      <div className="logs-subtoolbar">
        <div className="logs-status-group">
          <span
            className={`logs-live-status ${live ? (sseConnected ? 'is-connected' : 'is-connecting') : ''}`}
          >
            <i />
            {liveStatus}
          </span>
          {keyword.trim() ? <span className="logs-match-count">{filteredLines.length} 条匹配</span> : null}
          {current && !current.online ? <Tag color="orange">该节点离线，无法读取</Tag> : null}
        </div>

        <div className="logs-view-options">
          <Select
            size="small"
            value={displayMode}
            onChange={(value: DisplayMode) => setDisplayMode(value)}
            options={[
              { value: 'compact', label: '精简显示' },
              { value: 'raw', label: '原始日志' },
            ]}
          />
          <label className="logs-toggle logs-wrap-toggle">
            <Switch size="small" checked={wrapLines} onChange={setWrapLines} />
            <span>自动换行</span>
          </label>
          <Button
            size="small"
            icon={<DownloadOutlined />}
            disabled={!filteredLines.length}
            onClick={downloadLogs}
          >
            下载
          </Button>
        </div>
      </div>

      {filteredLines.length ? (
        <div className="logs-viewer-shell">
          <VirtualList
            ref={boxRef}
            items={filteredLines}
            getKey={logEntryKey}
            estimatedItemHeight={32}
            overscan={12}
            className={`logs-viewer ${wrapLines ? 'is-wrap' : 'is-nowrap'}`}
            onScroll={handleScroll}
            role="log"
            ariaLabel="sing-box 日志内容"
            renderItem={(line, index) => {
              const compact = displayMode === 'compact' && line.parsed
              const componentText = compactComponent(line.component || 'sing-box', line.direction)
              const messageText = compactMessage(line.message, line.direction)
              return (
                <div
                  className={`logs-line ${index % 2 ? 'is-even' : ''} ${compact ? 'logs-line-compact' : 'logs-line-raw'} logs-line-${levelClass(line.level)}`}
                  title={compact ? line.raw : undefined}
                >
                  {compact ? (
                    <>
                      <span className="logs-time">{line.time || '--:--:--'}</span>
                      <span className={`logs-level logs-level-${levelClass(line.level)}`}>
                        {line.level || 'LOG'}
                      </span>
                      <span className={`logs-direction logs-direction-${line.direction || 'none'}`}>
                        {line.direction === 'inbound' ? 'INBOUND' : line.direction === 'outbound' ? 'OUTBOUND' : '—'}
                      </span>
                      <span className={`logs-user ${line.user ? '' : 'is-empty'}`} title={line.user || undefined}>
                        {line.user ? highlightText(line.user, keyword) : null}
                      </span>
                      <span
                        className={`logs-component logs-component-${line.direction || 'neutral'}`}
                        title={line.component}
                      >
                        {highlightText(componentText, keyword)}
                      </span>
                      <span className="logs-context" title={line.context}>
                        {renderContext(line.context, keyword)}
                      </span>
                      <span className="logs-message">{renderSemanticText(messageText, keyword)}</span>
                    </>
                  ) : (
                    <span className="logs-raw-text">{renderSemanticText(line.raw, keyword)}</span>
                  )}
                  <button
                    type="button"
                    className="logs-copy-button"
                    onClick={() => copyLine(line.raw)}
                    title="复制该行"
                    aria-label="复制该行日志"
                  >
                    <CopyOutlined />
                  </button>
                </div>
              )
            }}
          />

          {!following ? (
            <Button
              className="logs-jump-latest"
              type="primary"
              size="small"
              icon={<VerticalAlignBottomOutlined />}
              onClick={() => scrollToLatest(true)}
            >
              回到最新
            </Button>
          ) : null}
        </div>
      ) : (
        <div className="logs-empty">
          <Empty description={emptyDescription} />
        </div>
      )}

      <div className="logs-footer">
        <span>
          已显示 {filteredLines.length} / {logEntries.length} 行
        </span>
        <span>最后更新：{formatUpdatedAt(lastUpdatedAt)}</span>
      </div>
    </Card>
  )
}
