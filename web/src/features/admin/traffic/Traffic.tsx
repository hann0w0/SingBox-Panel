import { useEffect, useState } from 'react'
import { Card, Empty, Select, message } from 'antd'
import { errMsg, listServers } from '../../../api'
import type { Server } from '../../../types'
import ServerTraffic from '../servers/ServerTraffic'

// Traffic is the standalone traffic page: pick a host, see its charts.
export default function Traffic() {
  const [servers, setServers] = useState<Server[]>([])
  const [serverId, setServerId] = useState<number | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    listServers(controller.signal)
      .then((items) => {
        if (controller.signal.aborted) return
        setServers(items)
        const first = items.find((server) => server.online) ?? items[0]
        if (first) setServerId((current) => current ?? first.id)
      })
      .catch((error) => {
        if (!controller.signal.aborted) message.error(errMsg(error))
      })
    return () => controller.abort()
  }, [])

  const selector = (
    <Select<number>
      value={serverId ?? undefined}
      onChange={setServerId}
      placeholder="选择主机"
      popupMatchSelectWidth={false}
      options={servers.map((server) => ({
        value: server.id,
        label: `${server.name}${server.online ? '' : '（离线）'}`,
      }))}
      style={{ minWidth: 220, width: '100%', maxWidth: 320 }}
    />
  )
  const current = servers.find((server) => server.id === serverId)

  if (!serverId || !current) {
    return (
      <Card title="流量" extra={selector}>
        <Empty description={servers.length ? '请选择主机' : '暂无主机'} />
      </Card>
    )
  }
  return <ServerTraffic key={serverId} serverId={serverId} titleExtra={selector} />
}
