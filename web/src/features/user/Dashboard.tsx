import { useCallback, useEffect, useRef, useState } from 'react'
import { Button, Form, Grid, Input, Modal, Segmented, Select, Typography, message } from '../../components/kit'
import { ChevronRight, Eye, EyeOff, Plus, Trash2 } from 'lucide-react'
import { QRCodeSVG } from 'qrcode.react'
import {
  createUserForward, deleteUserForward, deleteUserOutbound, errMsg, getMe, getMyTraffic, getUserForwards,
  getUserInbounds, getUserNodes, getUserOutbounds, importUserOutbound, isCanceledRequest, resetSub,
} from '../../lib/api'
import type { SubscriptionSyncResult, UserForward, UserInboundOption, UserNode, UserPrivateOutbound } from '../../lib/api'
import type { UserTrafficRange, UserTrafficSeries } from '../../lib/types'
import { formatBytes } from '../../lib/util'
import { SubscriptionLinks } from './SubscriptionLinks'
import { SubscriptionSyncStatus } from './SubscriptionSyncStatus'
import { useAuth } from '../../lib/store'
import regionData from '../../assets/regions.json'
import { VirtualList } from '../../components/VirtualList'
import { RequestState } from '../../components/RequestState'
import { RegionFlag } from '../../components/RegionFlag'
import { Pills, Section } from '../../components/kit/Page'
import '../../styles/settings-user.css'
import { MODAL_MD, MODAL_SM } from '../../components/kit/modal'
import { showConfigApplyResult } from '../../lib/configApply'

type RegionInfo = { geo: string; coord: [number, number]; label: string }
type UserTab = 'nodes' | 'outbounds' | 'forwards'
const REGIONS = regionData as unknown as Record<string, RegionInfo>
const userNodeKey = (node: UserNode) => node.id || `${node.type}:${node.name}:${node.server}:${node.port}`
const labelOf = (code: string): string => REGIONS[code]?.label || (code === 'Other' ? '其他' : code)

const SENSITIVE_PARAM_KEYS = new Set(['UUID', '密码', 'Obfs密码', 'PSK', '认证', '公钥', 'Short ID'])
const maskSecretValue = (val: string) => (val ? '••••••••' : '—')

function useDashboardRequest<T>(request: (signal: AbortSignal) => Promise<T>, initialData: T, enabled = true) {
  const [data, setData] = useState(initialData)
  const [loading, setLoading] = useState(enabled)
  const [error, setError] = useState<string | null>(null)
  const requestRef = useRef<{ generation: number; controller?: AbortController }>({ generation: 0 })
  const reload = useCallback(() => {
    requestRef.current.controller?.abort()
    const controller = new AbortController()
    const generation = ++requestRef.current.generation
    requestRef.current.controller = controller
    const current = () => generation === requestRef.current.generation && !controller.signal.aborted
    setLoading(true)
    setError(null)
    void request(controller.signal)
      .then((next) => { if (current()) setData(next) })
      .catch((cause) => { if (current() && !isCanceledRequest(cause)) setError(errMsg(cause)) })
      .finally(() => {
        if (!current()) return
        requestRef.current.controller = undefined
        setLoading(false)
      })
  }, [request])
  useEffect(() => {
    if (!enabled) {
      requestRef.current.generation++
      requestRef.current.controller?.abort()
      setLoading(false)
      return
    }
    reload()
    return () => {
      requestRef.current.generation++
      requestRef.current.controller?.abort()
    }
  }, [reload, enabled])
  return { data, setData, loading, error, reload }
}

export default function Dashboard() {
  const screens = Grid.useBreakpoint()
  const isMobile = !screens.md
  const setUser = useAuth((s) => s.setUser)
  const me = useDashboardRequest<Awaited<ReturnType<typeof getMe>> | null>(getMe, null)
  const user = me.data?.user
  const forwardingEnabled = !!user?.forwarding_enabled
  const [tab, setTab] = useState<UserTab>('nodes')
  const [subUrl, setSubUrl] = useState('')
  const [resetSync, setResetSync] = useState<SubscriptionSyncResult>()
  const [revealNodeSecrets, setRevealNodeSecrets] = useState(false)
  const nodeRequest = useDashboardRequest<UserNode[]>(getUserNodes, [])
  const outboundRequest = useDashboardRequest<UserPrivateOutbound[]>(getUserOutbounds, [], forwardingEnabled)
  const inboundRequest = useDashboardRequest<UserInboundOption[]>(getUserInbounds, [], forwardingEnabled)
  const forwardRequest = useDashboardRequest<UserForward[]>(getUserForwards, [], forwardingEnabled)
  const nodes = nodeRequest.data
  const outbounds = outboundRequest.data
  const inbounds = inboundRequest.data
  const forwards = forwardRequest.data
  const [trafficRange, setTrafficRange] = useState<UserTrafficRange>('24h')
  const fetchTraffic = useCallback((signal: AbortSignal) => getMyTraffic(trafficRange, signal), [trafficRange])
  const trafficRequest = useDashboardRequest<UserTrafficSeries | null>(fetchTraffic, null)
  const traffic = trafficRequest.data
  const [selectedNode, setSelectedNode] = useState<UserNode | null>(null)
  const [importOpen, setImportOpen] = useState(false)
  const [importMode, setImportMode] = useState<'uri' | 'subscription'>('uri')
  const [importForm] = Form.useForm<{ value: string }>()
  const [importing, setImporting] = useState(false)
  const [forwardOpen, setForwardOpen] = useState(false)
  const [forwardForm] = Form.useForm<{ inbound: number; outbound: number }>()
  const [forwarding, setForwarding] = useState(false)
  const [actionID, setActionID] = useState<number | null>(null)
  const [outboundActionID, setOutboundActionID] = useState<number | null>(null)
  const mounted = useRef(false)
  const importLock = useRef(false)
  const forwardLock = useRef(false)

  const load = () => {
    me.reload()
    nodeRequest.reload()
    if (forwardingEnabled) {
      outboundRequest.reload()
      inboundRequest.reload()
      forwardRequest.reload()
    }
  }
  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])
  useEffect(() => {
    if (me.data) {
      setSubUrl(me.data.subscription_url)
      setUser(me.data.user)
    }
  }, [me.data, setUser])

  if (!user) return <RequestState loading={me.loading} error={me.error} hasData={false} onRetry={me.reload}><span /></RequestState>

  const doReset = () => Modal.confirm({
    title: '重置订阅链接？',
    content: '旧链接将立即失效，并更换你在多用户节点上的连接凭证。旧节点配置在同步后失效；离线节点将在重连后同步，请更新客户端订阅。',
    okText: '重置', okButtonProps: { danger: true }, cancelText: '取消',
    onOk: async () => {
      try {
        const result = await resetSub()
        if (!mounted.current) return
        setSubUrl(result.subscription_url)
        setResetSync(result.sync)
        setSelectedNode(null)
        nodeRequest.setData([])
        load()
        message.success('订阅链接已重置，请更新客户端订阅')
      } catch (error) {
        message.error(errMsg(error))
        throw error
      }
    },
  })

  const submitImport = async () => {
    if (importLock.current) return
    importLock.current = true
    setImporting(true)
    try {
      const values = await importForm.validateFields()
      if (!mounted.current) return
      await importUserOutbound(importMode === 'uri' ? { uri: values.value.trim() } : { url: values.value.trim() })
      if (!mounted.current) return
      setImportOpen(false)
      importForm.resetFields()
      outboundRequest.reload()
      message.success('已添加出站')
    } catch (cause: any) {
      if (mounted.current && !cause?.errorFields) message.error(errMsg(cause))
    } finally {
      importLock.current = false
      if (mounted.current) setImporting(false)
    }
  }

  const submitForward = async () => {
    if (forwardLock.current) return
    forwardLock.current = true
    setForwarding(true)
    try {
      const values = await forwardForm.validateFields()
      const inbound = inbounds.find((item) => item.id === values.inbound && item.multi_user)
      if (!inbound || !outbounds.some((item) => item.id === values.outbound)) throw new Error('请选择有效的入口节点和出站')
      if (!mounted.current) return
      const result = await createUserForward({ inbound_server_id: inbound.server_id, inbound_tag: inbound.tag, outbound_id: values.outbound })
      if (!mounted.current) return
      setForwardOpen(false)
      load()
      showConfigApplyResult(result)
    } catch (cause: any) {
      if (mounted.current && !cause?.errorFields) message.error(errMsg(cause))
    } finally {
      forwardLock.current = false
      if (mounted.current) setForwarding(false)
    }
  }

  const removeForward = (row: UserForward) => Modal.confirm({
    title: '删除转发', content: `确认删除「${row.inbound_tag} → ${row.outbound_tag}」？`, okText: '删除', okButtonProps: { danger: true },
    onOk: async () => {
      setActionID(row.id)
      try {
        const result = await deleteUserForward(row.id)
        if (!mounted.current) return
        load()
        showConfigApplyResult(result)
      } finally { if (mounted.current) setActionID(null) }
    },
  })

  const removeOutbound = (row: UserPrivateOutbound) => Modal.confirm({
    title: '删除出站', content: `确认删除「${row.tag}」？`, okText: '删除', okButtonProps: { danger: true },
    onOk: async () => {
      setOutboundActionID(row.id)
      try {
        await deleteUserOutbound(row.id)
        if (!mounted.current) return
        outboundRequest.reload()
        message.success('已删除出站')
      } finally { if (mounted.current) setOutboundActionID(null) }
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
    <RequestState loading={me.loading} error={me.error} hasData onRetry={me.reload}>
      <div className="sub-page">
        <Section title="用量" actions={<Segmented<UserTrafficRange> options={[{ value: '24h', label: '24 小时' }, { value: '7d', label: '7 天' }, { value: '30d', label: '30 天' }]} value={trafficRange} onChange={setTrafficRange} />}>
          <RequestState loading={trafficRequest.loading} error={trafficRequest.error} hasData={!!traffic} onRetry={trafficRequest.reload}><div className="ui-card sub-usage">{traffic ? <div className="sub-usage-stats"><div><span>下载 ↓</span><strong className="mono">{formatBytes(traffic.download)}</strong></div><div><span>上传 ↑</span><strong className="mono">{formatBytes(traffic.upload)}</strong></div><div><span>合计</span><strong className="mono">{formatBytes(traffic.download + traffic.upload)}</strong></div></div> : <div className="ui-empty">暂无用量</div>}</div></RequestState>
        </Section>
        <Section title="订阅链接" actions={<Button size="small" onClick={doReset}>重置链接</Button>}><SubscriptionLinks url={subUrl} /><SubscriptionSyncStatus key={subUrl} initial={resetSync} /></Section>
        <Pills label="控制台视图" active={user.forwarding_enabled ? tab : 'nodes'} onChange={(key) => setTab(key as UserTab)} actions={
          user.forwarding_enabled && tab === 'outbounds'
            ? <Button size="small" icon={<Plus />} onClick={() => setImportOpen(true)}>添加出站</Button>
            : user.forwarding_enabled && tab === 'forwards'
              ? <Button size="small" icon={<Plus />} onClick={() => { forwardForm.resetFields(); setForwardOpen(true) }}>添加转发</Button>
              : undefined
        } items={[
          { key: 'nodes', label: '节点', count: nodes.length },
          { key: 'outbounds', label: '出站', count: outbounds.length, disabled: !user.forwarding_enabled },
          { key: 'forwards', label: '转发', count: forwards.length, disabled: !user.forwarding_enabled },
        ]} />
        {(user.forwarding_enabled ? tab : 'nodes') === 'nodes' && <Section><RequestState loading={nodeRequest.loading} error={nodeRequest.error} hasData={nodes.length > 0} empty={!nodeRequest.loading && !nodeRequest.error && nodes.length === 0} emptyDescription="暂无节点" onRetry={nodeRequest.reload}><div className="ui-card sub-nodes">{nodes.length > 0 ? (isMobile || nodes.length > 50 ? <VirtualList className="sub-node-virtual" style={{ height: Math.min(nodes.length, isMobile ? 6 : 9) * 64, maxHeight: isMobile ? 384 : 576 }} items={nodes} getKey={userNodeKey} estimatedItemHeight={64} overscan={5} renderItem={renderNode} /> : nodes.map((n, i) => <div key={userNodeKey(n)}>{renderNode(n, i)}</div>)) : <div className="ui-empty">暂无节点</div>}</div></RequestState></Section>}
        {user.forwarding_enabled && tab === 'outbounds' && <Section><RequestState loading={outboundRequest.loading} error={outboundRequest.error} hasData={outbounds.length > 0} empty={!outboundRequest.loading && !outboundRequest.error && outbounds.length === 0} emptyDescription="暂无出站" onRetry={outboundRequest.reload}><div className="ui-card sub-outbounds">{outbounds.map((outbound) => <div key={outbound.id} className="sub-outbound"><span className="sub-outbound-main"><strong>{outbound.tag}</strong>{outbound.remark && <small>{outbound.remark}</small>}</span><div className="flex items-center gap-1.5"><span className="ui-tag">{outbound.type}</span><Button size="small" type="text" icon={<Trash2 />} loading={outboundActionID === outbound.id} aria-label={`删除出站 ${outbound.tag}`} onClick={() => removeOutbound(outbound)} /></div></div>)}</div></RequestState></Section>}
        {user.forwarding_enabled && tab === 'forwards' && <Section><RequestState loading={forwardRequest.loading} error={forwardRequest.error} hasData={forwards.length > 0} empty={!forwardRequest.loading && !forwardRequest.error && forwards.length === 0} emptyDescription="暂无转发" onRetry={forwardRequest.reload}><div className="ui-card sub-outbounds">{forwards.map((row) => <div key={row.id} className="sub-outbound"><span className="sub-outbound-main"><strong>{row.node_name}</strong><small>{row.inbound_tag} → {row.outbound_tag} · {row.server_name}</small></span><Button size="small" type="text" icon={<Trash2 />} loading={actionID === row.id} aria-label={`删除转发 ${row.inbound_tag}`} onClick={() => removeForward(row)} /></div>)}</div></RequestState></Section>}
        <Modal title="添加出站" width={MODAL_SM} open={importOpen} onCancel={() => setImportOpen(false)} onOk={submitImport} confirmLoading={importing} okText="导入">
          <Form form={importForm} className="sub-form" onFinish={submitImport}>
            <Segmented<'uri' | 'subscription'> options={[{ value: 'uri', label: 'URI 导入' }, { value: 'subscription', label: '订阅导入' }]} value={importMode} onChange={setImportMode} />
            <Form.Item name="value" label={importMode === 'uri' ? '节点 URI' : '订阅地址'} rules={[{ required: true, whitespace: true, message: importMode === 'uri' ? '请输入节点 URI' : '请输入订阅地址' }]}><Input.TextArea autoSize={{ minRows: 3, maxRows: 6 }} className="mono" disabled={importing} /></Form.Item>
          </Form>
        </Modal>
        <Modal title="添加转发" width={MODAL_SM} open={forwardOpen} onCancel={() => setForwardOpen(false)} onOk={submitForward} confirmLoading={forwarding} okText="添加">
          <RequestState loading={inboundRequest.loading} error={inboundRequest.error} hasData={inbounds.length > 0} onRetry={inboundRequest.reload}><span /></RequestState>
          <RequestState loading={outboundRequest.loading} error={outboundRequest.error} hasData={outbounds.length > 0} onRetry={outboundRequest.reload}><span /></RequestState>
          <Form form={forwardForm} className="sub-form" onFinish={submitForward}>
            <Form.Item name="inbound" label="入口节点" rules={[{ required: true, message: '请选择入口节点' }]}><Select options={forwardInboundOptions.map((item) => ({ value: item.id, label: `${item.server_name} · ${item.tag}` }))} placeholder="选择入口节点" disabled={forwarding || (inboundRequest.loading && !inbounds.length)} /></Form.Item>
            <Form.Item name="outbound" label="出站" rules={[{ required: true, message: '请选择出站' }]}><Select options={outbounds.map((item) => ({ value: item.id, label: item.tag }))} placeholder="选择出站" disabled={forwarding || (outboundRequest.loading && !outbounds.length)} /></Form.Item>
          </Form>
        </Modal>
        <Modal
          title={selectedNode?.name}
          open={!!selectedNode}
          onCancel={() => { setSelectedNode(null); setRevealNodeSecrets(false) }}
          footer={
            <div className="flex items-center justify-between gap-2 w-full">
              <Button
                size="small"
                icon={revealNodeSecrets ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
                onClick={() => setRevealNodeSecrets((v) => !v)}
              >
                {revealNodeSecrets ? '隐藏凭据' : '显示凭据'}
              </Button>
              <Button onClick={() => { setSelectedNode(null); setRevealNodeSecrets(false) }}>关闭</Button>
            </div>
          }
          width={MODAL_MD}
        >
          {selectedNode && (
            <div className="sub-detail">
              {selectedNode.link && (
                <div className="sub-qr">
                  <div className="sub-qr-box"><QRCodeSVG value={selectedNode.link} size={184} /></div>
                  <span className="ui-tag">{selectedNode.type}</span>
                </div>
              )}
              <div className="ui-card">
                <dl className="ui-kv sub-params">
                  {Object.entries(selectedNode.params).map(([key, value]) => {
                    const masked = !revealNodeSecrets && SENSITIVE_PARAM_KEYS.has(key)
                    return (
                      <div key={key}>
                        <dt>{key}</dt>
                        <dd><Typography.Text className="mono" copyable={{ text: value }}>{masked ? maskSecretValue(value) : value}</Typography.Text></dd>
                      </div>
                    )
                  })}
                </dl>
              </div>
              {selectedNode.link && (
                <div className="sub-link">
                  <span className="sub-link-text mono">{revealNodeSecrets ? selectedNode.link : `${selectedNode.type}://••••••••`}</span>
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
