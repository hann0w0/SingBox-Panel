import { useEffect, useState } from 'react'
import { Alert, Card, Col, Row, Statistic, Table, Tag } from 'antd'
import { getServerTraffic } from '../../api'
import type { TrafficPoint, TrafficSummary } from '../../types'
import { formatBytes } from '../../util'

function rate(value: number): string {
  return `${formatBytes(value)}/s`
}

export function ServerTraffic({ serverId }: { serverId: number }) {
  const [data, setData] = useState<TrafficSummary | null>(null)

  useEffect(() => {
    let stopped = false
    const load = () => getServerTraffic(serverId, 30).then((value) => {
      if (!stopped) setData(value)
    }).catch(() => {})
    load()
    const timer = window.setInterval(load, 5000)
    return () => {
      stopped = true
      window.clearInterval(timer)
    }
  }, [serverId])

  const recent = (data?.history ?? []).slice(-7).reverse()
  return (
    <Card
      title="代理流量"
      extra={<Tag>{data?.retention_days ?? 400} 天统计保留</Tag>}
    >
      {data && !data.available && (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message="暂未收到 sing-box 流量数据"
          description="升级 Agent 并重新下发一次面板管理配置后会自动启用；原始配置模式需要自行配置仅监听本机的 Clash API。"
        />
      )}
      <Row gutter={[16, 16]}>
        <Col xs={12} md={6}><Statistic title="实时上传" value={rate(data?.upload_rate ?? 0)} /></Col>
        <Col xs={12} md={6}><Statistic title="实时下载" value={rate(data?.download_rate ?? 0)} /></Col>
        <Col xs={12} md={6}><Statistic title="今日上传" value={formatBytes(data?.today_upload ?? 0)} /></Col>
        <Col xs={12} md={6}><Statistic title="今日下载" value={formatBytes(data?.today_download ?? 0)} /></Col>
        <Col xs={12} md={6}><Statistic title="本月上传" value={formatBytes(data?.month_upload ?? 0)} /></Col>
        <Col xs={12} md={6}><Statistic title="本月下载" value={formatBytes(data?.month_download ?? 0)} /></Col>
        <Col xs={12} md={6}><Statistic title="累计上传" value={formatBytes(data?.upload ?? 0)} /></Col>
        <Col xs={12} md={6}><Statistic title="累计下载" value={formatBytes(data?.download ?? 0)} /></Col>
      </Row>
      <Table
        style={{ marginTop: 20 }}
        rowKey="date"
        size="small"
        pagination={false}
        dataSource={recent}
        locale={{ emptyText: '暂无历史流量' }}
        columns={[
          { title: '日期（UTC）', dataIndex: 'date' },
          { title: '上传', render: (_, item: TrafficPoint) => formatBytes(item.upload) },
          { title: '下载', render: (_, item: TrafficPoint) => formatBytes(item.download) },
          { title: '合计', render: (_, item: TrafficPoint) => formatBytes(item.upload + item.download) },
        ]}
      />
    </Card>
  )
}
