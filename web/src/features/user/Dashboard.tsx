import { useEffect, useRef, useState } from 'react'
import { Button, Grid, Modal, Typography, message } from '../../ui'
import { ChevronRight } from 'lucide-react'
import { QRCodeSVG } from 'qrcode.react'
import { errMsg, getMe, getUserNodes, resetSub } from '../../api'
import type { UserNode } from '../../api'
import type { User } from '../../types'
import { SubscriptionLinks } from './SubscriptionLinks'
import { useAuth } from '../../store'
import { CONTINENT_ORDER, continentOf, type Continent } from '../../continents'
import regionData from '../../assets/regions.json'
import { VirtualList } from '../../components/VirtualList'
import { RequestState } from '../../components/RequestState'
import { RegionFlag } from '../../components/RegionFlag'
import { Pills, Section } from '../../ui/Page'
import '../../styles/settings-user.css'
import { MODAL_MD } from '../../ui/modal'

type RegionInfo = { geo: string; coord: [number, number]; label: string }
const REGIONS = regionData as unknown as Record<string, RegionInfo>
const userNodeKey = (node: UserNode) => `${node.type}:${node.name}:${node.server}:${node.port}`

// Chinese label for an ISO region code (HK → 香港), falls back to the code.
const labelOf = (code: string): string => REGIONS[code]?.label || (code === 'Other' ? '其他' : code)

export default function Dashboard() {
  const screens = Grid.useBreakpoint()
  const isMobile = !screens.md
  const setUser = useAuth((s) => s.setUser)
  const [user, setLocalUser] = useState<User | null>(null)
  const [subUrl, setSubUrl] = useState('')
  const [nodes, setNodes] = useState<UserNode[]>([])
  const [continentNodes, setContinentNodes] = useState<Partial<Record<Continent, UserNode[]>>>({})
  const [seg, setSeg] = useState<Continent>('亚洲')
  const [selectedNode, setSelectedNode] = useState<UserNode | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [nodesLoading, setNodesLoading] = useState(true)
  const [nodesError, setNodesError] = useState<string | null>(null)
  const loadRef = useRef<{ generation: number; controllers: AbortController[] }>({ generation: 0, controllers: [] })

  const load = () => {
    loadRef.current.controllers.forEach((controller) => controller.abort())
    const meController = new AbortController()
    const nodesController = new AbortController()
    const generation = ++loadRef.current.generation
    loadRef.current.controllers = [meController, nodesController]
    setLoading(true)
    setNodesLoading(true)
    setLoadError(null)
    setNodesError(null)
    getMe(meController.signal)
      .then((d) => {
        if (generation !== loadRef.current.generation) return
        setLocalUser(d.user)
        setSubUrl(d.subscription_url)
        setUser(d.user)
      })
      .catch((e) => {
        if (generation === loadRef.current.generation && !meController.signal.aborted) setLoadError(errMsg(e))
      })
      .finally(() => {
        if (generation === loadRef.current.generation && !meController.signal.aborted) setLoading(false)
      })
    getUserNodes(nodesController.signal)
      .then((next) => {
        if (generation === loadRef.current.generation) setNodes(next)
      })
      .catch((e) => {
        if (generation === loadRef.current.generation && !nodesController.signal.aborted) setNodesError(errMsg(e))
      })
      .finally(() => {
        if (generation === loadRef.current.generation && !nodesController.signal.aborted) setNodesLoading(false)
      })
  }
  useEffect(() => {
    load()
    return () => {
      loadRef.current.generation++
      loadRef.current.controllers.forEach((controller) => controller.abort())
    }
  }, [])

  useEffect(() => {
    const byContinent: Partial<Record<Continent, UserNode[]>> = {}
    for (const n of nodes) {
      const c = continentOf(n.region)
      if (!byContinent[c]) byContinent[c] = []
      byContinent[c].push(n)
    }
    setContinentNodes(byContinent)
  }, [nodes])

  if (!user) {
    return <RequestState loading={loading} error={loadError} hasData={false} onRetry={load}><span /></RequestState>
  }

  const doReset = () => Modal.confirm({
    title: '重置订阅链接？',
    content: '旧链接将立即失效，并更换你在多用户节点上的连接凭证。旧节点配置在同步后失效；离线节点将在重连后同步，请更新客户端订阅。',
    okText: '重置',
    okButtonProps: { danger: true },
    cancelText: '取消',
    onOk: async () => {
      try {
        const result = await resetSub()
        setSubUrl(result.subscription_url)
        setSelectedNode(null)
        setNodes([])
        load()
        message.success('订阅链接已重置，节点凭证正在同步，请更新客户端订阅')
      } catch (error) {
        message.error(errMsg(error))
        throw error
      }
    },
  })
  // Only show continents that actually have nodes; keep the current segment
  // valid if it becomes empty after a refresh.
  const segOptions = CONTINENT_ORDER
    .map((c) => ({ value: c, count: continentNodes[c]?.length ?? 0 }))
    .filter((o) => o.count > 0)
    .map((o) => ({ key: o.value, label: o.value, count: o.count }))
  const activeSeg = segOptions.some((o) => o.key === seg) ? seg : (segOptions[0]?.key ?? '其他')
  const activeNodes = continentNodes[activeSeg] ?? []
  const renderNode = (n: UserNode, index = 0) => (
    <div
      onClick={() => setSelectedNode(n)}
      onKeyDown={(event) => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault()
          setSelectedNode(n)
        }
      }}
      role="button"
      tabIndex={0}
      className={`sub-node${index === 0 ? ' is-first' : ''}`}
    >
      <RegionFlag code={n.region} size={15} fallback={false} />
      <div className="sub-node-main">
        <span className="sub-node-name">{n.name}</span>
        <span className="sub-node-meta">
          {labelOf(n.region || '') && <span>{labelOf(n.region || '')}</span>}
          <span className="mono">{n.server}:{n.port}</span>
        </span>
      </div>
      <span className="ui-tag sub-node-type">{n.type}</span>
      <ChevronRight className="sub-node-chev" aria-hidden="true" />
    </div>
  )

  return (
    <RequestState loading={loading} error={loadError} hasData onRetry={load}>
      <div className="sub-page">
        <Section title="订阅链接">
          <SubscriptionLinks url={subUrl} />
          <div className="sub-actions">
            <Button onClick={doReset}>重置链接</Button>
          </div>
        </Section>

        <Section title="节点">
          <RequestState loading={nodesLoading} error={nodesError} hasData={nodes.length > 0} empty={!nodesLoading && !nodesError && nodes.length === 0} emptyDescription="暂无节点" onRetry={load}>
            {segOptions.length > 1 && (
              <Pills label="地区" items={segOptions} active={activeSeg} onChange={(key) => setSeg(key as Continent)} />
            )}
            <div className="ui-card sub-nodes">
              {activeNodes.length > 0 ? (
                isMobile || activeNodes.length > 50 ? (
                  <VirtualList
                    className="sub-node-virtual"
                    style={{ height: Math.min(activeNodes.length, isMobile ? 6 : 9) * 64, maxHeight: isMobile ? 384 : 576 }}
                    items={activeNodes}
                    getKey={userNodeKey}
                    estimatedItemHeight={64}
                    overscan={5}
                    renderItem={renderNode}
                  />
                ) : (
                  activeNodes.map((n, i) => <div key={userNodeKey(n)}>{renderNode(n, i)}</div>)
                )
              ) : (
                <div className="ui-empty">该地区暂无节点</div>
              )}
            </div>
          </RequestState>
        </Section>

        <Modal title={selectedNode?.name} open={!!selectedNode} onCancel={() => setSelectedNode(null)} footer={<Button onClick={() => setSelectedNode(null)}>关闭</Button>} width={MODAL_MD}>
          {selectedNode && (
            <div className="sub-detail">
              {selectedNode.link && (
                <div className="sub-qr">
                  <div className="sub-qr-box">
                    <QRCodeSVG value={selectedNode.link} size={184} />
                  </div>
                  <span className="ui-tag">{selectedNode.type}</span>
                </div>
              )}
              <div className="ui-card">
                <dl className="ui-kv sub-params">
                  {Object.entries(selectedNode.params).map(([k, v]) => (
                    <div key={k}>
                      <dt>{k}</dt>
                      <dd><Typography.Text className="mono" copyable={{ text: v }}>{v}</Typography.Text></dd>
                    </div>
                  ))}
                </dl>
              </div>
              {selectedNode.link && (
                <div className="sub-link">
                  <span className="sub-link-text mono">{selectedNode.link}</span>
                  <Typography.Text copyable={{ text: selectedNode.link, tooltips: ['复制链接', '已复制'] }} />
                </div>
              )}
            </div>
          )}
        </Modal>
      </div>
    </RequestState>
  )
}
