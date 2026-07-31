import { useCallback, useEffect, useRef, useState } from 'react'
import { Button, Card, Col, Divider, Empty, Grid, Row, Segmented, Select, Space, Tag, Typography, message } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { errMsg, getServerTraffic, listServers } from '../../api'
import type { Server, TrafficRange, TrafficSeries } from '../../types'
import TrafficChart, { type TrafficChartSeries } from './TrafficChart'

const { Text, Title } = Typography

const RANGE_OPTIONS: { label: string; value: TrafficRange }[] = [
  { label: '1h', value: '1h' },
  { label: '12h', value: '12h' },
  { label: '24h', value: '24h' },
  { label: '7day', value: '7d' },
  { label: '30day', value: '30d' },
]

const NETWORK_SERIES: TrafficChartSeries[] = [
  { key: 'download', label: '下载', color: '#4096ff' },
  { key: 'upload', label: '上传', color: '#36cfc9' },
]
const UPLOAD_SERIES: TrafficChartSeries[] = [{ key: 'upload', label: '上传', color: '#36cfc9' }]
const DOWNLOAD_SERIES: TrafficChartSeries[] = [{ key: 'download', label: '下载', color: '#4096ff' }]

export default function Traffic() {
  const screens = Grid.useBreakpoint()
  const compact = !screens.sm
  const [servers, setServers] = useState<Server[]>([])
  const [serverId, setServerId] = useState<number | null>(null)
  const [range, setRange] = useState<TrafficRange>('24h')
  const [series, setSeries] = useState<TrafficSeries | null>(null)
  const [loading, setLoading] = useState(false)
  const requestRef = useRef(0)
  const loadingRequestRef = useRef(0)

  useEffect(() => {
    listServers()
      .then((items) => {
        setServers(items)
        const first = items.find((item) => item.online) ?? items[0]
        if (first) setServerId(first.id)
      })
      .catch((error) => message.error(errMsg(error)))
  }, [])

  const load = useCallback(async (quiet = false) => {
    if (!serverId) return
    const requestId = ++requestRef.current
    if (!quiet) {
      loadingRequestRef.current = requestId
      setLoading(true)
    }
    try {
      const data = await getServerTraffic(serverId, range)
      if (requestId === requestRef.current) setSeries(data)
    } catch (error) {
      if (!quiet) message.error(errMsg(error))
    } finally {
      if (!quiet && requestId === loadingRequestRef.current) setLoading(false)
    }
  }, [range, serverId])

  useEffect(() => {
    setSeries(null)
    load()
  }, [load])

  useEffect(() => {
    if (!serverId) return
    const timer = window.setInterval(() => load(true), 30_000)
    return () => window.clearInterval(timer)
  }, [load, serverId])

  const selectedServer = servers.find((server) => server.id === serverId)
  const hasHistory = series?.points.some((point) => point.upload > 0 || point.download > 0) ?? false
  const showData = Boolean(series && (series.available || hasHistory))
  const updatedAt = series?.updated_at
    ? new Date(series.updated_at).toLocaleString('zh-CN', { hour12: false })
    : '尚未上报'

  return (
    <Card
      title="流量统计"
      size={compact ? 'small' : 'default'}
      extra={<Button icon={<ReloadOutlined />} loading={loading} onClick={() => load()} title="刷新" aria-label="刷新" />}
      styles={{ body: { padding: compact ? '14px 12px 18px' : '18px 20px 24px' } }}
    >
      <Row gutter={[20, 16]} align="middle" justify="space-between">
        <Col xs={24} lg="auto">
          <div style={{ maxWidth: '100%', overflowX: 'auto', paddingBottom: 1 }}>
            <Segmented<TrafficRange> options={RANGE_OPTIONS} value={range} onChange={setRange} />
          </div>
        </Col>
        <Col xs={24} lg="auto">
          <Space wrap size={compact ? 6 : 10} style={{ width: compact ? '100%' : undefined }}>
            <Select
              style={{ width: compact ? 'calc(100vw - 118px)' : 260, maxWidth: compact ? 260 : '100%' }}
              value={serverId ?? undefined}
              onChange={setServerId}
              placeholder="选择节点"
              options={servers.map((server) => ({
                value: server.id,
                label: `${server.name}${server.region ? ` · ${server.region}` : ''}${server.online ? '' : '（离线）'}`,
              }))}
            />
            {selectedServer ? <Tag color={selectedServer.online ? 'success' : 'warning'}>{selectedServer.online ? '在线' : '离线'}</Tag> : null}
          </Space>
        </Col>
      </Row>

      <Divider style={{ marginBlock: compact ? 14 : 18 }} />

      {!serverId ? (
        <Empty description={servers.length ? '请选择节点' : '暂无节点'} />
      ) : series && !showData ? (
        <Empty description="升级 Agent 并重新下发面板管理配置后开始采集" />
      ) : series && showData ? (
        <>
          <Space wrap size={6} style={{ marginBottom: compact ? 4 : 8 }}>
            <Text type="secondary" style={{ fontSize: 12 }}>最后更新：{updatedAt}</Text>
            {!series.available && hasHistory ? <Tag color="warning">节点当前未上报，显示历史流量</Tag> : null}
          </Space>

          <section>
            <Title level={5} style={{ margin: compact ? '4px 0 0' : '8px 0 0', fontSize: compact ? 14 : undefined }}>网络平均速率</Title>
            <TrafficChart
              points={series.points}
              range={range}
              series={NETWORK_SERIES}
              valueMode="rate"
              stepSeconds={series.step_seconds}
              ariaLabel="节点上传和下载平均速率趋势图"
            />
          </section>

          <Divider style={{ marginBlock: compact ? 12 : 16 }} />

          <section>
            <Title level={5} style={{ margin: 0, fontSize: compact ? 14 : undefined }}>上传流量</Title>
            <TrafficChart points={series.points} range={range} series={UPLOAD_SERIES} ariaLabel="节点上传流量趋势图" />
          </section>

          <Divider style={{ marginBlock: compact ? 12 : 16 }} />

          <section>
            <Title level={5} style={{ margin: 0, fontSize: compact ? 14 : undefined }}>下载流量</Title>
            <TrafficChart points={series.points} range={range} series={DOWNLOAD_SERIES} ariaLabel="节点下载流量趋势图" />
          </section>
        </>
      ) : (
        <div style={{ minHeight: 320, display: 'grid', placeItems: 'center', color: '#8c8c8c' }}>
          {loading ? '正在读取流量数据...' : '暂无流量数据'}
        </div>
      )}
    </Card>
  )
}
