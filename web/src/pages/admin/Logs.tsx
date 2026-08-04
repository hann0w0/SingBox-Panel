import { useEffect, useRef, useState, useCallback } from 'react'
import { Button, Card, Empty, Input, Select, Space, Switch, Tag, message } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { errMsg, listServers, serverLogs, streamLogs } from '../../api'
import type { Server } from '../../types'
import { useSSE } from '../../useSSE'
import type { SSEMessage } from '../../useSSE'

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

export default function Logs() {
  const [servers, setServers] = useState<Server[]>([])
  const [sid, setSid] = useState<number | null>(null)
  const [lines, setLines] = useState(200)
  const [text, setText] = useState('')
  const [loading, setLoading] = useState(false)
  const [live, setLive] = useState(false)
  const [hideNoise, setHideNoise] = useState(true)
  const [keyword, setKeyword] = useState('')
  const boxRef = useRef<HTMLPreElement>(null)

  useEffect(() => {
    listServers()
      .then((s) => {
        setServers(s)
        const first = s.find((x) => x.online) ?? s[0]
        if (first) setSid(first.id)
      })
      .catch((e) => message.error(errMsg(e)))
  }, [])

  const load = async (quiet = false) => {
    if (!sid) return
    if (!quiet) setLoading(true)
    try {
      const t = await serverLogs(sid, lines)
      setText(t)
      requestAnimationFrame(() => {
        if (boxRef.current) boxRef.current.scrollTop = boxRef.current.scrollHeight
      })
    } catch (e) {
      if (!quiet) message.error(errMsg(e))
    } finally {
      if (!quiet) setLoading(false)
    }
  }

  // SSE live log streaming: connect to the same SSE endpoint and filter for
  // kind === "log" messages. The agent-side journalctl -f is started/stopped
  // via the streamLogs API call below.
  const liveUrl = sid ? `/api/admin/servers/${sid}/traffic/live` : ''
  const onSSEMessage = useCallback((msg: SSEMessage) => {
    if (msg.kind !== 'log' || !msg.data) return
    const logEvt = msg.data as { level: string; msg: string }
    setText((prev) => {
      const next = prev + (prev ? '\n' : '') + logEvt.msg
      const lineArr = next.split('\n')
      if (lineArr.length > MAX_LIVE_LINES) {
        return lineArr.slice(lineArr.length - MAX_LIVE_LINES).join('\n')
      }
      return next
    })
    requestAnimationFrame(() => {
      if (boxRef.current) boxRef.current.scrollTop = boxRef.current.scrollHeight
    })
  }, [])
  const { connected: sseConnected } = useSSE(liveUrl, live && !!sid, onSSEMessage)

  // Start/stop agent-side journalctl -f when live toggle changes.
  useEffect(() => {
    if (!sid) return
    if (live) {
      streamLogs(sid, { enable: true, lines }).catch((e) => {
        message.error(errMsg(e))
        setLive(false)
      })
    } else {
      streamLogs(sid, { enable: false }).catch(() => {})
    }
  }, [live, sid])

  // When switching servers or line count, stop live and reload.
  useEffect(() => {
    setLive(false)
    setText('')
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sid, lines])

  const filteredText = (() => {
    if (!text) return ''
    let lineArr = text.split('\n')
    if (hideNoise) {
      lineArr = lineArr.filter((line) => !NOISE_PATTERNS.some((p) => line.includes(p)))
    }
    if (keyword.trim()) {
      const kw = keyword.trim().toLowerCase()
      lineArr = lineArr.filter((line) => line.toLowerCase().includes(kw))
    }
    return lineArr.join('\n')
  })()

  const current = servers.find((s) => s.id === sid)

  return (
    <Card
      title="sing-box 日志"
      extra={
        <Button icon={<ReloadOutlined />} loading={loading} onClick={() => load()} title="刷新" aria-label="刷新" />
      }
    >
      <Space wrap style={{ marginBottom: 12 }}>
        <Select
          style={{ minWidth: 200 }}
          value={sid ?? undefined}
          onChange={setSid}
          placeholder="选择节点"
          options={servers.map((s) => ({
            value: s.id,
            label: `${s.name}${s.region ? ' · ' + s.region : ''}${s.online ? '' : '（离线）'}`,
            disabled: !s.online,
          }))}
        />
        <Select
          style={{ width: 120 }}
          value={lines}
          onChange={setLines}
          options={LINE_OPTIONS.map((n) => ({ value: n, label: `最近 ${n} 行` }))}
        />
        <Input
          placeholder="搜索关键词日志"
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
          style={{ width: 160 }}
          allowClear
        />
        <Space size={6}>
          <Switch checked={hideNoise} onChange={setHideNoise} />
          <span style={{ fontSize: 13, color: 'rgba(0,0,0,0.65)' }}>过滤扫描噪音</span>
        </Space>
        <Space size={6}>
          <Switch checked={live} onChange={setLive} disabled={!current?.online} />
          <span style={{ fontSize: 13, color: 'rgba(0,0,0,0.65)' }}>
            实时推送{live ? (sseConnected ? ' · 已连接' : ' · 连接中…') : ''}
          </span>
        </Space>
        {current && !current.online ? <Tag color="orange">该节点离线，无法读取</Tag> : null}
      </Space>

      {filteredText ? (
        <pre
          ref={boxRef}
          style={{
            margin: 0,
            maxHeight: '62vh',
            overflow: 'auto',
            background: '#0f172a',
            color: '#e2e8f0',
            padding: '12px 14px',
            borderRadius: 8,
            fontSize: 12,
            lineHeight: 1.6,
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-all',
          }}
        >
          {filteredText}
        </pre>
      ) : (
        <Empty description={loading ? '读取中…' : text ? '已自动过滤全部扫描垃圾日志' : '暂无日志'} />
      )}
    </Card>
  )
}
