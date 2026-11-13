import { useEffect, useRef, useState } from 'react'
import { Button, Grid, Input, Modal, Segmented, Select, Typography, message } from '../../ui'
import { ChevronRight, Plus, Trash2 } from 'lucide-react'
import { QRCodeSVG } from 'qrcode.react'
import {
  createUserForward, deleteUserForward, errMsg, getMe, getMyTraffic, getUserForwards,
  getUserInbounds, getUserNodes, getUserOutbounds, importUserOutbound, resetSub,
} from '../../api'
import type { SubscriptionSyncResult, UserForward, UserInboundOption, UserNode, UserPrivateOutbound } from '../../api'
import type { User, UserTrafficRange, UserTrafficSeries } from '../../types'
import { formatBytes } from '../../util'
import { SubscriptionLinks } from './SubscriptionLinks'
import { SubscriptionSyncStatus } from './SubscriptionSyncStatus'
import { useAuth } from '../../store'
import regionData from '../../assets/regions.json'
import { VirtualList } from '../../components/VirtualList'
import { RequestState } from '../../components/RequestState'
import { RegionFlag } from '../../components/RegionFlag'
import { Pills, Section } from '../../ui/Page'
import '../../styles/settings-user.css'
import { MODAL_MD, MODAL_SM } from '../../ui/modal'

type RegionInfo = { geo: string; coord: [number, number]; label: string }
type UserTab = 'nodes' | 'outbounds' | 'forwards'
const REGIONS = regionData as unknown as Record<string, RegionInfo>
const userNodeKey = (node: UserNode) => `${node.type}:${node.name}:${node.server}:${node.port}`
const labelOf = (code: string): string => REGIONS[code]?.label || (code === 'Other' ? '其他' : code)

export default function Dashboard() {
  const screens = Grid.useBreakpoint()
  const isMobile = !screens.md
  const setUser = useAuth((s) => s.setUser)
  const [user, setLocalUser] = useState<User | null>(null)
  const [tab, setTab] = useState<UserTab>('nodes')
  const [subUrl, setSubUrl] = useState('')
  const [resetSync, setResetSync] = useState<SubscriptionSyncResult>()
  const [nodes, setNodes] = useState<UserNode[]>([])
  const [outbounds, setOutbounds] = useState<UserPrivateOutbound[]>([])
  const [inbounds, setInbounds] = useState<UserInboundOption[]>([])
  const [forwards, setForwards] = useState<UserForward[]>([])
  const [traffic, setTraffic] = useState<UserTrafficSeries | null>(null)
  const [trafficRange, setTrafficRange] = useState<UserTrafficRange>('24h')
  const [trafficLoading, setTrafficLoading] = useState(true)
  const [selectedNode, setSelectedNode] = useState<UserNode | null>(null)
  const [importOpen, setImportOpen] = useState(false)
  const [importMode, setImportMode] = useState<'uri' | 'subscription'>('uri')
  const [importValue, setImportValue] = useState('')
  const [importing, setImporting] = useState(false)
  const [forwardOpen, setForwardOpen] = useState(false)
  const [forwardInbound, setForwardInbound] = useState<number | undefined>()
  const [forwardOutbound, setForwardOutbound] = useState<number | undefined>()
  const [forwarding, setForwarding] = useState(false)
  const [actionID, setActionID] = useState<number | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [nodesLoading, setNodesLoading] = useState(true)
  const [nodesError, setNodesError] = useState<string | null>(null)
  const loadRef = useRef<{ generation: number; controllers: AbortController[] }>({ generation: 0, controllers: [] })

  const load = () => {
    loadRef.current.controllers.forEach((controller) => controller.abort())
    const meController = new AbortController()
    const nodesController = new AbortController()
    const outboundsController = new AbortController()
    const trafficController = new AbortController()
    const generation = ++loadRef.current.generation
    loadRef.current.controllers = [meController, nodesController, outboundsController, trafficController]
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
      .then((next) => { if (generation === loadRef.current.generation) setNodes(next) })
      .catch((e) => { if (generation === loadRef.current.generation && !nodesController.signal.aborted) setNodesError(errMsg(e)) })
      .finally(() => { if (generation === loadRef.current.generation && !nodesController.signal.aborted) setNodesLoading(false) })
    getUserOutbounds(outboundsController.signal).then(setOutbounds).catch(() => setOutbounds([]))
    getUserInbounds(outboundsController.signal).then(setInbounds).catch(() => setInbounds([]))
    getUserForwards(outboundsController.signal).then(setForwards).catch(() => setForwards([]))
    setTrafficLoading(true)
    getMyTraffic(trafficRange, trafficController.signal)
      .then(setTraffic)
      .catch(() => setTraffic(null))
      .finally(() => { if (generation === loadRef.current.generation && !trafficController.signal.aborted) setTrafficLoading(false) })
  }
  useEffect(() => {
    load()
    return () => {
      loadRef.current.generation++
      loadRef.current.controllers.forEach((controller) => controller.abort())
    }
  }, [trafficRange])

  if (!user) return <RequestState loading={loading} error={loadError} hasData={false} onRetry={load}><span /></RequestState>

  const doReset = () => Modal.confirm({
    title: '重置订阅链接？',
    content: '旧链接将立即失效，并更换你在多用户节点上的连接凭证。旧节点配置在同步后失效；离线节点将在重连后同步，请更新客户端订阅。',
    okText: '重置', okButtonProps: { danger: true }, cancelText: '取消',
    onOk: async () => {
      try {
        const result = await resetSub()
        setSubUrl(result.subscription_url)
        setResetSync(result.sync)
        setSelectedNode(null)
        setNodes([])
        load()
        message.success('订阅链接已重置，请更新客户端订阅')
      } catch (error) {
        message.error(errMsg(error))
        throw error
      }
    },
  })

  const submitImport = async () => {
    if (!importValue.trim()) return
    setImporting(true)
    try {
      await importUserOutbound(importMode === 'uri' ? { uri: importValue.trim() } : { url: importValue.trim() })
      setImportOpen(false)
      setImportValue('')
      load()
      message.success('已添加出站')
    } catch (cause) { message.error(errMsg(cause)) } finally { setImporting(false) }
  }

  const submitForward = async () => {
    if (!forwardInbound || !forwardOutbound) return
    const inbound = inbounds.find((item) => item.id === forwardInbound)
    if (!inbound) return
    setForwarding(true)
    try {
      await createUserForward({ inbound_server_id: inbound.server_id, inbound_tag: inbound.tag, outbound_id: forwardOutbound })
      setForwardOpen(false)
      load()
      message.success('已添加转发')
    } catch (cause) { message.error(errMsg(cause)) } finally { setForwarding(false) }
  }

  const removeForward = (row: UserForward) => Modal.confirm({
    title: '删除转发', content: `确认删除「${row.inbound_tag} → ${row.outbound_tag}」？`, okText: '删除', okButtonProps: { danger: true },
    onOk: async () => {
      setActionID(row.id)
      try { await deleteUserForward(row.id); load() } finally { setActionID(null) }
    },
  })

  const renderNode = (n: UserNode, index = 0) => (
    <div onClick={() => setSelectedNode(n)} onKeyDown={(event) => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); setSelectedNode(n) } }} role="button" tabIndex={0} className={`sub-node${index === 0 ? ' is-first' : ''}`}>
      <RegionFlag code={n.region} size={15} fallback={false} />
      <div className="sub-node-main"><span className="sub-node-name">{n.name}</span><span className="sub-node-meta">{labelOf(n.region || '') && <span>{labelOf(n.region || '')}</span>}<span className="mono">{n.server}:{n.port}</span></span></div>
      <span className="ui-tag sub-node-type">{n.type}</span><ChevronRight className="sub-node-chev" aria-hidden="true" />
    </div>
  )

  const forwardInboundOptions = inbounds.filter((item) => item.multi_user)

  return (
    <RequestState loading={loading} error={loadError} hasData onRetry={load}>
      <div className="sub-page">
        <Section title="用量" actions={<Segmented<UserTrafficRange> options={[{ value: '24h', label: '24 小时' }, { value: '7d', label: '7 天' }, { value: '30d', label: '30 天' }]} value={trafficRange} onChange={setTrafficRange} />}>
          <div className="ui-card sub-usage">{trafficLoading && !traffic ? <div className="ui-empty">读取中…</div> : traffic ? <div className="sub-usage-stats"><div><span>下载 ↓</span><strong className="mono">{formatBytes(traffic.download)}</strong></div><div><span>上传 ↑</span><strong className="mono">{formatBytes(traffic.upload)}</strong></div><div><span>合计</span><strong className="mono">{formatBytes(traffic.download + traffic.upload)}</strong></div></div> : <div className="ui-empty">暂无用量</div>}</div>
        </Section>
        <Section title="订阅链接" actions={<Button size="small" onClick={doReset}>重置链接</Button>}><SubscriptionLinks url={subUrl} /><SubscriptionSyncStatus key={subUrl} initial={resetSync} /></Section>
        <Pills label="控制台视图" active={user.forwarding_enabled ? tab : 'nodes'} onChange={(key) => setTab(key as UserTab)} actions={
          user.forwarding_enabled && tab === 'outbounds'
            ? <Button size="small" icon={<Plus />} onClick={() => setImportOpen(true)}>添加出站</Button>
            : user.forwarding_enabled && tab === 'forwards'
              ? <Button size="small" icon={<Plus />} onClick={() => { setForwardInbound(undefined); setForwardOutbound(undefined); setForwardOpen(true) }}>添加转发</Button>
              : undefined
        } items={[
          { key: 'nodes', label: '节点', count: nodes.length },
          { key: 'outbounds', label: '出站', count: outbounds.length, disabled: !user.forwarding_enabled },
          { key: 'forwards', label: '转发', count: forwards.length, disabled: !user.forwarding_enabled },
        ]} />
        {(user.forwarding_enabled ? tab : 'nodes') === 'nodes' && <Section><RequestState loading={nodesLoading} error={nodesError} hasData={nodes.length > 0} empty={!nodesLoading && !nodesError && nodes.length === 0} emptyDescription="暂无节点" onRetry={load}><div className="ui-card sub-nodes">{nodes.length > 0 ? (isMobile || nodes.length > 50 ? <VirtualList className="sub-node-virtual" style={{ height: Math.min(nodes.length, isMobile ? 6 : 9) * 64, maxHeight: isMobile ? 384 : 576 }} items={nodes} getKey={userNodeKey} estimatedItemHeight={64} overscan={5} renderItem={renderNode} /> : nodes.map((n, i) => <div key={userNodeKey(n)}>{renderNode(n, i)}</div>)) : <div className="ui-empty">暂无节点</div>}</div></RequestState></Section>}
        {user.forwarding_enabled && tab === 'outbounds' && <Section>{outbounds.length === 0 ? <div className="ui-empty ui-card">暂无出站</div> : <div className="ui-card sub-outbounds">{outbounds.map((outbound) => <div key={outbound.id} className="sub-outbound"><span className="sub-outbound-main"><strong>{outbound.tag}</strong>{outbound.remark && <small>{outbound.remark}</small>}</span><span className="ui-tag">{outbound.type}</span></div>)}</div>}</Section>}
        {user.forwarding_enabled && tab === 'forwards' && <Section>{forwards.length === 0 ? <div className="ui-empty ui-card">暂无转发</div> : <div className="ui-card sub-outbounds">{forwards.map((row) => <div key={row.id} className="sub-outbound"><span className="sub-outbound-main"><strong>{row.node_name}</strong><small>{row.inbound_tag} → {row.outbound_tag} · {row.server_name}</small></span><Button size="small" type="text" icon={<Trash2 />} loading={actionID === row.id} aria-label={`删除转发 ${row.inbound_tag}`} onClick={() => removeForward(row)} /></div>)}</div>}</Section>}
        <Modal title="添加出站" width={MODAL_SM} open={importOpen} onCancel={() => setImportOpen(false)} onOk={submitImport} confirmLoading={importing} okText="导入">
          <div className="sub-form">
            <Segmented<'uri' | 'subscription'> options={[{ value: 'uri', label: 'URI 导入' }, { value: 'subscription', label: '订阅导入' }]} value={importMode} onChange={setImportMode} />
            <label>{importMode === 'uri' ? '节点 URI' : '订阅地址'}<Input.TextArea autoSize={{ minRows: 3, maxRows: 6 }} className="mono" value={importValue} onChange={(event) => setImportValue(event.target.value)} placeholder={importMode === 'uri' ? 'vless://…' : 'https://…'} /></label>
          </div>
        </Modal>
        <Modal title="添加转发" width={MODAL_SM} open={forwardOpen} onCancel={() => setForwardOpen(false)} onOk={submitForward} confirmLoading={forwarding} okText="添加">
          <div className="sub-form">
            <label>入口节点<Select options={forwardInboundOptions.map((item) => ({ value: item.id, label: `${item.server_name} · ${item.tag}` }))} value={forwardInbound} onChange={setForwardInbound} placeholder="选择入口节点" /></label>
            <label>出站<Select options={outbounds.map((item) => ({ value: item.id, label: item.tag }))} value={forwardOutbound} onChange={setForwardOutbound} placeholder="选择出站" /></label>
          </div>
        </Modal>
        <Modal title={selectedNode?.name} open={!!selectedNode} onCancel={() => setSelectedNode(null)} footer={<Button onClick={() => setSelectedNode(null)}>关闭</Button>} width={MODAL_MD}>{selectedNode && <div className="sub-detail">{selectedNode.link && <div className="sub-qr"><div className="sub-qr-box"><QRCodeSVG value={selectedNode.link} size={184} /></div><span className="ui-tag">{selectedNode.type}</span></div>}<div className="ui-card"><dl className="ui-kv sub-params">{Object.entries(selectedNode.params).map(([key, value]) => <div key={key}><dt>{key}</dt><dd><Typography.Text className="mono" copyable={{ text: value }}>{value}</Typography.Text></dd></div>)}</dl></div>{selectedNode.link && <div className="sub-link"><span className="sub-link-text mono">{selectedNode.link}</span><Typography.Text copyable={{ text: selectedNode.link, tooltips: ['复制链接', '已复制'] }} /></div>}</div>}</Modal>
      </div>
    </RequestState>
  )
}
