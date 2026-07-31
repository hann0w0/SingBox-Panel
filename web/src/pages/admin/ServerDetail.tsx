import { useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Form,
  Input,
  List,
  Modal,
  Popconfirm,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd'
import {
  CheckOutlined,
  ControlOutlined,
  CopyOutlined,
  DeleteOutlined,
  EditOutlined,
  EyeOutlined,
  LockOutlined,
  MenuOutlined,
  PlusOutlined,
  ReloadOutlined,
} from '@ant-design/icons'
import { useParams } from 'react-router-dom'
import {
  applyRawConfig,
  deleteInbound,
  deleteOutbound,
  deleteRule,
  errMsg,
  getNodeFormats,
  getServer,
  importConfig,
  installSingbox,
  listOutbounds,
  listRuleSets,
  listRules,
  remoteConfig,
  reorderRules,
  serverStatus,
  serviceAction,
  setConfigMode,
  setFinalOutbound,
  testEgress,
  testOutbound,
  uninstallAgent,
  updateAgent,
} from '../../api'
import type { EgressTest, ImportSummary, NodeFormats, OutboundTest } from '../../api'
import { isConfigApplyResult, showConfigApplyResult } from '../../configApply'
import type { Inbound, Outbound, RouteRule, RuleSet, Server } from '../../types'
import { formatBytes } from '../../util'
import InboundForm from './InboundForm'
import { OutboundForm, RuleForm } from './RoutingForms'
import { RuleSetsTab } from './RuleSetsTab'

export default function ServerDetail() {
  const { id } = useParams()
  const sid = Number(id)
  const [server, setServer] = useState<Server | null>(null)
  const [install, setInstall] = useState('')
  const [publicURL, setPublicURL] = useState('')
  const [busy, setBusy] = useState('')
  const [refreshing, setRefreshing] = useState(false)
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<Inbound | null>(null)
  const [installForm] = Form.useForm()
  const [installOpen, setInstallOpen] = useState(false)
  const [cfgOpen, setCfgOpen] = useState(false)
  const [cfgText, setCfgText] = useState('')
  const [cfgLoading, setCfgLoading] = useState(false)
  const [cfgSaving, setCfgSaving] = useState(false)

  // 节点格式弹窗状态
  const [fmtOpen, setFmtOpen] = useState(false)
  const [fmtLoading, setFmtLoading] = useState(false)
  const [fmtData, setFmtData] = useState<NodeFormats | null>(null)

  const [outbounds, setOutbounds] = useState<Outbound[]>([])
  const [rules, setRules] = useState<RouteRule[]>([])
  const [ruleSets, setRuleSets] = useState<RuleSet[]>([])
  const [obOpen, setObOpen] = useState(false)
  const [obEditing, setObEditing] = useState<Outbound | null>(null)
  const [ruleOpen, setRuleOpen] = useState(false)
  const [ruleEditing, setRuleEditing] = useState<RouteRule | null>(null)
  const [rsModalOpen, setRsModalOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [importSum, setImportSum] = useState<ImportSummary | null>(null)
  const [importing, setImporting] = useState(false)
  const [obTests, setObTests] = useState<Record<number, OutboundTest>>({})
  const [testing, setTesting] = useState<number | null>(null)
  // Egress latency is a property of the node, so one result covers every inbound.
  const [egress, setEgress] = useState<EgressTest | null>(null)
  const [egressBusy, setEgressBusy] = useState(false)

  const load = async () => {
    const d = await getServer(sid)
    setServer(d.server)
    setInstall(d.install_command)
    setPublicURL(d.public_url)
    const [outboundRows, ruleRows, ruleSetRows] = await Promise.allSettled([
      listOutbounds(sid),
      listRules(sid),
      listRuleSets(sid),
    ])
    if (outboundRows.status === 'fulfilled') setOutbounds(outboundRows.value)
    if (ruleRows.status === 'fulfilled') setRules(ruleRows.value)
    if (ruleSetRows.status === 'fulfilled') setRuleSets(ruleSetRows.value)
  }

  const [draggedRuleIndex, setDraggedRuleIndex] = useState<number | null>(null)

  const handleRuleDrop = async (dropIndex: number) => {
    if (draggedRuleIndex === null || draggedRuleIndex === dropIndex) return
    const newRules = [...rules]
    const [draggedItem] = newRules.splice(draggedRuleIndex, 1)
    newRules.splice(dropIndex, 0, draggedItem)
    setRules(newRules)
    setDraggedRuleIndex(null)

    const order = newRules.map((r) => r?.id).filter((id): id is number => typeof id === 'number' && id > 0)
    if (!order.length) return
    try {
      const res = await reorderRules(sid, order)
      showConfigApplyResult(res)
    } catch (e) {
      message.error(errMsg(e))
      load()
    }
  }

  const moveRule = async (index: number, direction: 'up' | 'down') => {
    const targetIndex = direction === 'up' ? index - 1 : index + 1
    if (targetIndex < 0 || targetIndex >= rules.length) return
    const newRules = [...rules]
    const temp = newRules[index]
    newRules[index] = newRules[targetIndex]
    newRules[targetIndex] = temp
    setRules(newRules)
    const order = newRules.map((r) => r.id)
    try {
      const res = await reorderRules(sid, order)
      showConfigApplyResult(res)
    } catch (e) {
      message.error(errMsg(e))
      load()
    }
  }


  useEffect(() => {
    load().catch((e) => message.error(errMsg(e)))
  }, [sid])

  const refresh = async () => {
    setRefreshing(true)
    try {
      if (server?.online) await serverStatus(sid)
      await load()
      if (server?.online) message.success('已从 Agent 获取最新状态')
      else message.info('节点当前离线，已刷新面板状态')
    } catch (e) {
      await load().catch(() => {})
      message.error(errMsg(e))
    } finally {
      setRefreshing(false)
    }
  }

  const outboundTags = outbounds.map((o) => o.tag)
  const inboundTags = (server?.inbounds || []).map((i) => i.tag)

  const onSetFinal = (v: string) => run('final', () => setFinalOutbound(sid, v), 'final 出站已更新')

  const switchToManaged = () =>
    Modal.confirm({
      title: '切换到面板管理配置?',
      content: '切换后会立即用面板中的入站、出站和路由记录覆盖服务器原始配置；面板无法识别的字段将不再生效。',
      okText: '确认切换并下发',
      okType: 'danger',
      onOk: () => run('mode', () => setConfigMode(sid, 'managed'), '已切换到面板管理'),
    })

  // Import: preview what the server's own config.json contains, then adopt it.
  const openImport = async () => {
    setBusy('import')
    try {
      const r = await importConfig(sid, { dry_run: true })
      setImportSum(r.summary)
      setImportOpen(true)
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setBusy('')
    }
  }
  const doImport = async () => {
    setImporting(true)
    try {
      await importConfig(sid, {})
      message.success('已导入到面板')
      setImportOpen(false)
      load()
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setImporting(false)
    }
  }

  // Reachability is measured from the node, not from the panel.
  const runTest = async (o: Outbound) => {
    setTesting(o.id)
    try {
      const r = await testOutbound(sid, o.id)
      setObTests((prev) => ({ ...prev, [o.id]: r }))
      if (r.ok) message.success(`${o.tag} 可达 · ${r.latency_ms}ms`)
      else message.error(`${o.tag} 不通：${r.error || '连接失败'}`)
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setTesting(null)
    }
  }

  // The install command is long and wraps badly inline, so it lives behind a
  // button and is shown (and copied) in a dialog instead.
  const showInstallCmd = () =>
    Modal.info({
      title: '安装 / 升级 Agent',
      width: 600,
      icon: null,
      content: (
        <div>
          <div style={{ fontSize: 13, color: 'rgba(0, 0, 0, 0.45)', marginBottom: 12 }}>
            以 root 在该服务器上执行。首次执行为安装，已安装时重复执行即为升级。
          </div>
          <div style={{ position: 'relative' }}>
            <pre
              style={{
                background: '#f8fafc',
                border: '1px solid #e2e8f0',
                borderRadius: 6,
                padding: '12px 14px',
                fontSize: 12,
                lineHeight: 1.75,
                color: '#1e293b',
                fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                whiteSpace: 'pre-wrap',
                wordBreak: 'break-all',
                margin: 0,
              }}
            >
              {install}
            </pre>
            <Button
              size="small"
              icon={<CopyOutlined />}
              style={{ position: 'absolute', top: 8, right: 8 }}
              onClick={() =>
                navigator.clipboard.writeText(install).then(() => message.success('已复制'))
              }
            />
          </div>
        </div>
      ),
    })

  const installOrUpgradeAgent = () => {
    if (!server?.online) {
      showInstallCmd()
      return
    }
    Modal.confirm({
      title: '自动升级 Agent?',
      okText: '下发升级',
      content: '面板将向在线节点下发最新版 Agent。新版本会先完成校验，重启后若无法重新连接将自动恢复旧版本。',
      onOk: async () => {
        setBusy('agent')
        try {
          await updateAgent(sid)
          message.success('Agent 已接收升级，正在重启并重新连接')
          window.setTimeout(load, 4000)
        } catch (e) {
          const text = errMsg(e)
          Modal.error({
            title: '自动升级失败',
            width: 680,
            content: (
              <Typography.Paragraph copyable={{ text }} style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                {text}
              </Typography.Paragraph>
            ),
          })
        } finally {
          setBusy('')
        }
      },
    })
  }

  const runEgressTest = async () => {
    setEgressBusy(true)
    try {
      const r = await testEgress(sid)
      setEgress(r)
      if (r.ok) message.success(`出口正常 · ${r.latency_ms}ms`)
      else message.error(`出口异常：${r.error || 'HTTP ' + r.status}`)
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setEgressBusy(false)
    }
  }

  const onDeleteInbound = (ib: Inbound) =>
    Modal.confirm({
      title: `删除入站 ${ib.tag}?`,
      okType: 'danger',
      okText: '确定',
      cancelText: '取消',
      centered: true,
      onOk: () => run('del', () => deleteInbound(sid, ib.id), '已删除'),
    })

  const onDeleteOutbound = (o: Outbound) =>
    Modal.confirm({
      title: `删除出站 ${o.tag}?`,
      okType: 'danger',
      okText: '确定',
      cancelText: '取消',
      centered: true,
      onOk: () => run('del', () => deleteOutbound(sid, o.id), '已删除'),
    })

  const onDeleteRule = (r: RouteRule) =>
    Modal.confirm({
      title: `确认删除该路由规则?`,
      okType: 'danger',
      okText: '确定',
      cancelText: '取消',
      centered: true,
      onOk: () => run('del', () => deleteRule(sid, r.id), '已删除'),
    })

  const onResetFinal = () =>
    Modal.confirm({
      title: '确认要重置兜底规则出站为 direct 吗？',
      okType: 'danger',
      okText: '确定',
      cancelText: '取消',
      centered: true,
      onOk: () => onSetFinal('direct'),
    })

  const run = async (label: string, fn: () => Promise<unknown>, ok = '操作完成') => {
    setBusy(label)
    try {
      const result = await fn()
      if (isConfigApplyResult(result)) showConfigApplyResult(result)
      else message.success(ok)
      load()
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setBusy('')
    }
  }

  const doInstall = async () => {
    const v = await installForm.validateFields()
    setInstallOpen(false)
    run('install', async () => {
      const r = await installSingbox(sid, v)
      Modal.info({ title: '安装输出', width: 640, content: <pre style={{ whiteSpace: 'pre-wrap' }}>{r.output || 'ok'}</pre> })
    }, '安装完成')
  }

  const openConfigEditor = async () => {
    setCfgOpen(true)
    setCfgLoading(true)
    setCfgText('')
    try {
      const cfg = await remoteConfig(sid)
      let text = cfg.raw || ''
      try {
        text = JSON.stringify(JSON.parse(text), null, 2)
      } catch {
        // leave raw text as-is if it isn't valid JSON
      }
      setCfgText(cfg.exists ? text : '')
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setCfgLoading(false)
    }
  }

  const saveConfig = async () => {
    setCfgSaving(true)
    try {
      await applyRawConfig(sid, cfgText)
      message.success('已校验并下发')
      setCfgOpen(false)
      load()
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setCfgSaving(false)
    }
  }



  if (!server) return null
  const rawMode = server.config_mode === 'raw'
  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Card
        title={
          <Space wrap style={{ rowGap: 4 }}>
            {server.name}
            {server.online ? <Tag color="green">在线</Tag> : <Tag>离线</Tag>}
            {server.singbox_installed ? <Tag color="blue">{server.singbox_version}</Tag> : <Tag color="orange">未装 sing-box</Tag>}
            {server.singbox_active ? <Tag color="green">服务运行</Tag> : <Tag color="red">服务未运行</Tag>}
          </Space>
        }
        extra={<Button icon={<ReloadOutlined />} loading={refreshing} onClick={refresh} title="刷新节点状态" aria-label="刷新节点状态" />}
      >
        <Descriptions column={{ xs: 1, sm: 2, lg: 3 }} size="small" bordered>
          <Descriptions.Item label="节点地址">
            <Space size={6} wrap>
              <span>{server.address || '—'}</span>
              {server.public_ip && server.public_ip !== server.address && (
                <Typography.Text type="secondary">({server.public_ip})</Typography.Text>
              )}
            </Space>
          </Descriptions.Item>
          <Descriptions.Item label="通信地址">{server.agent_url || publicURL || '旧版 Agent 未上报'}</Descriptions.Item>
          <Descriptions.Item label="Agent">{server.agent_version || '旧版未上报'}</Descriptions.Item>
          <Descriptions.Item label="系统">{server.os || '—'}</Descriptions.Item>
          <Descriptions.Item label="负载">{server.load1?.toFixed(2) ?? '—'}</Descriptions.Item>
          <Descriptions.Item label="内存">{formatBytes(server.mem_used)}/{formatBytes(server.mem_total)}</Descriptions.Item>
        </Descriptions>

        <Space wrap style={{ marginTop: 16 }}>
          <Button type="primary" loading={busy === 'install'} onClick={() => setInstallOpen(true)}>
            安装 / 升级 Sing-box
          </Button>
          <Button type="primary" loading={busy === 'agent'} onClick={installOrUpgradeAgent}>安装 / 升级 Agent</Button>
          <Button
            type="primary"
            danger
            disabled={!server.online}
            onClick={() =>
              Modal.confirm({
                title: '卸载被控端 Agent?',
                okText: '确认卸载 Agent',
                okType: 'danger',
                content: (
                  <Alert
                    type="warning"
                    showIcon
                    message="将删除 Agent 二进制及开机自启服务。"
                    description="面板节点配置均会保留，之后仍可使用 Agent 安装命令重新接入。"
                  />
                ),
                onOk: async () => {
                  try {
                    await uninstallAgent(sid)
                    message.success('Agent 正在卸载；面板节点和 sing-box 已保留')
                    window.setTimeout(load, 6000)
                  } catch (e) {
                    message.error(errMsg(e))
                    throw e
                  }
                },
              })
            }
          >
            卸载 Agent
          </Button>
          <Button loading={busy === 'import'} onClick={openImport}>识别配置</Button>
          <Button onClick={openConfigEditor}>编辑配置</Button>
          <Button
            loading={busy === 'restart'}
            onClick={() => run('restart', () => serviceAction(sid, 'restart'), 'sing-box 已重启')}
          >
            重启服务
          </Button>
          <Button
            loading={fmtLoading}
            onClick={async () => {
              setFmtOpen(true)
              if (fmtData) return // 已缓存
              setFmtLoading(true)
              try {
                const d = await getNodeFormats(sid)
                setFmtData(d)
              } catch (e) {
                message.error(errMsg(e))
                setFmtOpen(false)
              } finally {
                setFmtLoading(false)
              }
            }}
          >
            导出节点
          </Button>
        </Space>

      </Card>

      {rawMode && (
        <Alert
          type="warning"
          showIcon
          message="该节点当前使用原始配置模式"
          description="服务器完整 config.json 已保存到面板，Agent 重连时会原样下发。为避免丢失面板无法识别的字段，协议、出站和路由编辑暂时锁定。"
          action={<Button danger loading={busy === 'mode'} onClick={switchToManaged}>切换到面板管理</Button>}
        />
      )}

      <Card
        title="入站"
        extra={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            disabled={rawMode}
            onClick={() => {
              setEditing(null)
              setFormOpen(true)
            }}
          >
            新增协议
          </Button>
        }
      >
        <div style={{ fontSize: 13, color: 'rgba(0, 0, 0, 0.45)', marginBottom: 12 }}>
          配置节点接收客户端连接的入站协议。生成的节点信息将自动提供给订阅与客户端使用。
        </div>
        <Table
          rowKey="id"
          pagination={false}
          scroll={{ x: 560 }}
          dataSource={server.inbounds || []}
          columns={[
            { title: '标签', dataIndex: 'tag' },
            { title: '协议', dataIndex: 'type', render: (v) => <Tag>{v}</Tag> },
            { title: '端口', dataIndex: 'listen_port' },
            {
              title: '启用',
              dataIndex: 'enabled',
              render: (v: boolean) => (v ? <Tag color="green">是</Tag> : <Tag>否</Tag>),
            },
            {
              title: '出口延迟',
              render: () => {
                if (!egress) return <span style={{ color: '#bbb' }}>未测试</span>
                return egress.ok
                  ? <Tag color="green">通 · {egress.latency_ms}ms</Tag>
                  : <Tag color="red" title={egress.error}>不通</Tag>
              },
            },
            {
              title: '操作',
              render: (_, ib: Inbound) => (
                <Space>
                  <Button size="small" type="link" loading={egressBusy} onClick={runEgressTest}>测试</Button>
                  <Button size="small" type="link" disabled={rawMode} onClick={() => { setEditing(ib); setFormOpen(true) }}>编辑</Button>
                  <Button size="small" type="link" danger disabled={rawMode} onClick={() => onDeleteInbound(ib)}>删除</Button>
                </Space>
              ),
            },
          ]}
        />
      </Card>

      <Card
        title="出站"
        extra={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            disabled={rawMode}
            onClick={() => { setObEditing(null); setObOpen(true) }}
          >
            新增出站
          </Button>
        }
      >
        <div style={{ fontSize: 13, color: 'rgba(0, 0, 0, 0.45)', marginBottom: 12 }}>
          配置流量路由的目标出站。direct 为内置直连出站，无需重复添加；在「规则」中可将指定流量指向对应出站。
        </div>
        <Table
          rowKey="id"
          pagination={false}
          scroll={{ x: 560 }}
          dataSource={outbounds}
          columns={[
            { title: '标签', dataIndex: 'tag' },
            { title: '类型', dataIndex: 'type', render: (v) => <Tag>{v}</Tag> },
            { title: '目标地址', render: (_, o: Outbound) => `${o.settings?.server || '—'}:${o.settings?.server_port || ''}` },
            {
              title: '连通性',
              render: (_, o: Outbound) => {
                const t = obTests[o.id]
                if (!t) return <span style={{ color: '#bbb' }}>未测试</span>
                return t.ok
                  ? <Tag color="green">通 · {t.latency_ms}ms</Tag>
                  : <Tag color="red" title={t.error}>不通</Tag>
              },
            },
            {
              title: '操作',
              render: (_, o: Outbound) => (
                <Space>
                  <Button size="small" type="link" loading={testing === o.id} onClick={() => runTest(o)}>测试</Button>
                  <Button size="small" type="link" disabled={rawMode} onClick={() => { setObEditing(o); setObOpen(true) }}>编辑</Button>
                  <Button size="small" type="link" danger disabled={rawMode} onClick={() => onDeleteOutbound(o)}>删除</Button>
                </Space>
              ),
            },
          ]}
        />
      </Card>

      <Card
        title="规则"
        extra={
          <Space>
            <Button icon={<ControlOutlined />} disabled={rawMode} onClick={() => setRsModalOpen(true)}>
              规则集
            </Button>
            <Button
              type="primary"
              icon={<PlusOutlined />}
              disabled={rawMode}
              onClick={() => { setRuleEditing(null); setRuleOpen(true) }}
            >
              新增规则
            </Button>
          </Space>
        }
      >
        <div style={{ fontSize: 13, color: 'rgba(0, 0, 0, 0.45)', marginBottom: 12 }}>
          按条件匹配入站流量并路由至指定出站。规则从上至下依次顺序匹配，未命中的流量走兜底出站。
        </div>

        <Table
          rowKey="id"
          pagination={false}
          scroll={{ x: 640 }}
          dataSource={rules}
          onRow={(_, index) => ({
            draggable: !rawMode,
            onDragStart: (e) => {
              setDraggedRuleIndex(index!);
              e.dataTransfer.effectAllowed = 'move';
              e.dataTransfer.setData('text/plain', String(index));
            },
            onDragOver: (e) => e.preventDefault(),
            onDragEnd: () => setDraggedRuleIndex(null),
            onDrop: () => handleRuleDrop(index!),
            style: { cursor: 'grab' },
          })}
          columns={[
            {
              title: '排序',
              width: 70,
              align: 'center',
              render: () => (
                <MenuOutlined
                  style={{ color: '#94a3b8', fontSize: 14, cursor: 'grab' }}
                  title="按住拖拽以调整规则顺序"
                />
              ),
            },
            {
              title: '匹配条件',
              render: (_, r: RouteRule) => {
                const m = r.match || {}
                return (
                  <Space wrap size={[4, 4]}>
                    {m.rule_set?.map((tag) => (
                      <Tag key={tag} style={{ background: '#f0f5ff', color: '#2f54eb', borderColor: '#adc6ff' }}>
                        规则集: {tag}
                      </Tag>
                    ))}
                    {m.inbound?.map((ib) => (
                      <Tag key={ib} style={{ background: '#f6ffed', color: '#389e0d', borderColor: '#b7eb8f' }}>
                        入站: {ib}
                      </Tag>
                    ))}
                    {m.domain_suffix?.map((ds) => (
                      <Tag key={ds} style={{ background: '#fff7e6', color: '#d46b08', borderColor: '#ffd591' }}>
                        域名: {ds}
                      </Tag>
                    ))}
                    {m.ip_cidr?.map((ip) => (
                      <Tag key={ip} style={{ background: '#fff0f6', color: '#c41d7f', borderColor: '#ffadd2' }}>
                        IP: {ip}
                      </Tag>
                    ))}
                    {m.protocol?.map((p) => (
                      <Tag key={p} style={{ background: '#f9f0ff', color: '#722ed1', borderColor: '#d3ade6' }}>
                        协议: {p}
                      </Tag>
                    ))}
                    {!m.rule_set?.length && !m.inbound?.length && !m.domain_suffix?.length && !m.ip_cidr?.length && !m.protocol?.length && (
                      <Tag style={{ background: '#f5f5f5', color: '#595959', borderColor: '#d9d9d9' }}>
                        不限入站
                      </Tag>
                    )}
                  </Space>
                )
              },
            },
            {
              title: '动作 / 目标出站',
              dataIndex: 'outbound',
              width: 170,
              render: (v, r: RouteRule) => {
                const act = (r.match?.action as string) || (v === 'block' ? 'reject' : v)
                if (act === 'sniff' || v === 'sniff') return <Tag color="purple">sniff (协议嗅探)</Tag>
                if (act === 'reject' || v === 'block' || v === 'reject') return <Tag color="red">reject (拒绝访问)</Tag>
                if (act === 'hijack-dns' || v === 'hijack-dns') return <Tag color="orange">hijack-dns (DNS劫持)</Tag>
                return <Tag color="blue">{v}</Tag>
              },
            },
            {
              title: '启用',
              dataIndex: 'enabled',
              width: 80,
              render: (v: boolean) => (v ? <Tag color="green">是</Tag> : <Tag>否</Tag>),
            },
            {
              title: '操作',
              width: 110,
              render: (_, r: RouteRule) => (
                <Space size="small">
                  <Button size="small" type="link" disabled={rawMode} onClick={() => { setRuleEditing(r); setRuleOpen(true) }}>编辑</Button>
                  <Button size="small" type="link" danger disabled={rawMode} onClick={() => onDeleteRule(r)}>删除</Button>
                </Space>
              ),
            },
          ]}
          summary={() => {
            const finalOut = (server.final_outbound || 'direct').toUpperCase()
            return (
              <Table.Summary.Row style={{ background: '#fafafa' }}>
                <Table.Summary.Cell index={0} align="center">
                  <span style={{ color: '#d9d9d9' }}>-</span>
                </Table.Summary.Cell>
                <Table.Summary.Cell index={1}>
                  <Tag color="purple" style={{ margin: 0, fontWeight: 600 }}>
                    final
                  </Tag>
                </Table.Summary.Cell>
                <Table.Summary.Cell index={2}>
                  <Tag color={finalOut === 'DIRECT' ? 'geekblue' : 'blue'} style={{ margin: 0, fontWeight: 600 }}>
                    {finalOut}
                  </Tag>
                </Table.Summary.Cell>
                <Table.Summary.Cell index={3}>
                  <Tag color="green">是</Tag>
                </Table.Summary.Cell>
                <Table.Summary.Cell index={4}>
                  <Space size="small">
                    <Button size="small" type="link" disabled>
                      编辑
                    </Button>
                    <Button size="small" type="link" danger disabled={rawMode} onClick={onResetFinal}>
                      删除
                    </Button>
                  </Space>
                </Table.Summary.Cell>
              </Table.Summary.Row>
            )
          }}
        />
      </Card>

      <Modal
        title="规则集管理"
        open={rsModalOpen}
        onCancel={() => setRsModalOpen(false)}
        footer={null}
        width={580}
        centered
        destroyOnClose
      >
        <RuleSetsTab
          serverId={sid}
          ruleSets={ruleSets}
          outbounds={outbounds}
          onRefresh={load}
          rawMode={rawMode}
        />
      </Modal>

      <InboundForm
        serverId={sid}
        inbound={editing}
        open={formOpen}
        onClose={() => setFormOpen(false)}
        onSaved={load}
      />

      <OutboundForm
        serverId={sid}
        outbound={obEditing}
        open={obOpen}
        onClose={() => setObOpen(false)}
        onSaved={load}
      />

      <RuleForm
        serverId={sid}
        rule={ruleEditing}
        outboundTags={outboundTags}
        inboundTags={inboundTags}
        ruleSets={ruleSets}
        open={ruleOpen}
        onClose={() => setRuleOpen(false)}
        onSaved={load}
      />

      <Modal
        title="识别服务器配置"
        open={importOpen}
        onCancel={() => setImportOpen(false)}
        onOk={doImport}
        okText="确认导入面板"
        confirmLoading={importing}
        width={720}
        destroyOnClose
      >
        <div style={{ fontSize: 13, color: 'rgba(0, 0, 0, 0.45)', marginBottom: 12 }}>
          已识别当前运行配置。导入后完整原始配置将同步保存至面板。
        </div>
        {importSum && (
          <Space direction="vertical" size="middle" style={{ width: '100%' }}>
            <div>
              <b>入站协议 {importSum.inbounds?.length || 0} 个</b>
              <Table
                size="small"
                rowKey="tag"
                pagination={false}
                dataSource={importSum.inbounds || []}
                columns={[
                  { title: '标签', dataIndex: 'tag' },
                  { title: '协议', dataIndex: 'type', render: (v) => <Tag>{v}</Tag> },
                  { title: '端口', dataIndex: 'listen_port' },
                  {
                    title: '模式',
                    render: (_, r: { single_user: boolean; users: number }) =>
                      r.single_user ? <Tag color="green">单用户</Tag> : <Tag color="orange">多用户 {r.users}</Tag>,
                  },
                ]}
              />
            </div>
            <div>
              <b>出站 {importSum.outbounds?.length || 0} 个</b>
              <Table
                size="small"
                rowKey="tag"
                pagination={false}
                dataSource={importSum.outbounds || []}
                columns={[
                  { title: '标签', dataIndex: 'tag' },
                  { title: '类型', dataIndex: 'type', render: (v) => <Tag>{v}</Tag> },
                  { title: '目标地址', dataIndex: 'info' },
                ]}
              />
            </div>
            <div>
              <b>分流规则 {importSum.rules?.length || 0} 条</b>
              <Table
                size="small"
                rowKey={(_, i) => String(i)}
                pagination={false}
                dataSource={importSum.rules || []}
                columns={[
                  { title: '匹配入站', render: (_, r: { inbound: string[] | null }) => (r.inbound?.length ? r.inbound.join(', ') : '不限') },
                  { title: '其它匹配', dataIndex: 'info', render: (v) => v || '—' },
                  { title: '→ 出站', dataIndex: 'outbound', render: (v) => <Tag color="blue">{v}</Tag> },
                ]}
              />
            </div>
            <div style={{ color: '#555' }}>
              默认出站 final：<Tag>{importSum.final}</Tag>
            </div>
            {!!importSum.skipped?.length && (
              <div style={{ color: '#d46b08' }}>
                以下内容不会转换成结构化表单，但会保存在完整原始配置中：
                <ul style={{ margin: '4px 0 0 18px' }}>
                  {importSum.skipped.map((s) => <li key={s}>{s}</li>)}
                </ul>
              </div>
            )}
          </Space>
        )}
      </Modal>

      <Modal
        title="编辑服务器配置"
        open={cfgOpen}
        onCancel={() => setCfgOpen(false)}
        onOk={saveConfig}
        okText="校验并下发"
        confirmLoading={cfgSaving}
        width={780}
        destroyOnClose
      >
        <div style={{ fontSize: 13, color: 'rgba(0, 0, 0, 0.45)', marginBottom: 12 }}>
          直接编辑该服务器运行的核心配置。下发成功后完整 JSON 会保存至面板并同步生效。
        </div>
        {cfgLoading ? (
          <div style={{ padding: 24, textAlign: 'center', color: '#94a3b8' }}>读取中…</div>
        ) : (
          <Input.TextArea
            value={cfgText}
            onChange={(e) => setCfgText(e.target.value)}
            autoSize={{ minRows: 18, maxRows: 30 }}
            style={{ fontFamily: 'monospace', fontSize: 12 }}
            placeholder="该服务器暂无配置，可在此粘贴或编写完整 config.json"
          />
        )}
      </Modal>

      <Modal title="安装 / 升级 Sing-box" open={installOpen} onOk={doInstall} onCancel={() => setInstallOpen(false)} destroyOnClose>
        <div style={{ fontSize: 13, color: 'rgba(0, 0, 0, 0.45)', marginBottom: 12 }}>
          安装官方最新稳定版 sing-box；已安装时再次执行即升级到目标版本。
        </div>
        <Form form={installForm} layout="vertical" initialValues={{ channel: 'stable', method: 'script' }}>
          <Form.Item name="channel" label="渠道">
            <Select options={[{ value: 'stable', label: 'stable' }, { value: 'beta', label: 'beta' }]} />
          </Form.Item>
          <Form.Item name="method" label="安装方式">
            <Select
              options={[
                { value: 'script', label: '官方安装脚本' },
                { value: 'apt', label: '官方 APT 源' },
                { value: 'dnf', label: '官方 DNF 源' },
              ]}
            />
          </Form.Item>
          <Form.Item name="version" label="指定版本">
            <Input placeholder="1.13.14" />
          </Form.Item>
        </Form>
      </Modal>

      {/* 节点格式弹窗 */}
      <Modal
        title="导出节点配置"
        open={fmtOpen}
        onCancel={() => setFmtOpen(false)}
        footer={null}
        width={860}
        destroyOnClose
      >
        {fmtLoading ? (
          <div style={{ padding: 48, textAlign: 'center', color: '#bbb' }}>加载中…</div>
        ) : fmtData ? (
          <NodeFormatsModalContent data={fmtData} />
        ) : null}
      </Modal>

    </Space>
  )
}

// 协议颜色映射
const PROTO_COLORS: Record<string, string> = {
  vless: 'blue', vmess: 'geekblue', trojan: 'purple',
  ss: 'cyan', shadowsocks: 'cyan', hysteria2: 'orange',
  hysteria: 'volcano', tuic: 'gold', anytls: 'magenta',
  snell: 'green', naive: 'lime',
}

function NodeFormatsModalContent({ data }: { data: NodeFormats }) {
  const [copiedKey, setCopiedKey] = useState<string | null>(null)
  const [detailItem, setDetailItem] = useState<NodeFormats['items'][number] | null>(null)

  const handleCopy = (text: string, key: string, label: string) => {
    if (!text) return
    navigator.clipboard.writeText(text).then(() => {
      setCopiedKey(key)
      message.success(`已复制 ${label}`)
      setTimeout(() => setCopiedKey(null), 1800)
    })
  }

  const items = data.items || []

  return (
    <div style={{ paddingTop: 4 }}>
      <div style={{ fontSize: 13, color: 'rgba(0, 0, 0, 0.45)', marginBottom: 12 }}>
        提示：点击“详情”查看节点地址、端口和协议参数；其他按钮可复制客户端配置。
      </div>
      {!items.length ? (
        <div style={{ padding: '36px 0', textAlign: 'center', color: '#94a3b8' }}>
          暂无可导出的入站，请先添加协议
        </div>
      ) : (
        <List
          size="small"
          bordered
          dataSource={items}
          renderItem={(item, idx) => {
            const uriKey = `uri-${idx}`
            const clashKey = `clash-${idx}`
            const surgeKey = `surge-${idx}`

            const displayTag = item.tag || item.name || ''

            return (
              <List.Item
                style={{
                  padding: '12px 16px',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  flexWrap: 'wrap',
                  gap: '8px 12px',
                }}
              >
                {/* 左侧：仅保留干净入站协议标签 */}
                <div style={{ display: 'flex', alignItems: 'center', minWidth: 0 }}>
                  <span style={{ fontSize: 14, fontWeight: 500, color: '#1e293b', wordBreak: 'break-all' }}>
                    {displayTag}
                  </span>
                </div>

                {/* 右侧：详情与客户端配置复制按钮 */}
                <Space size={8} align="center" wrap>
                  <Button
                    size="small"
                    icon={<EyeOutlined />}
                    style={{
                      borderRadius: 6,
                      fontSize: 12,
                      padding: '0 12px',
                      height: 28,
                    }}
                    onClick={() => setDetailItem(item)}
                  >
                    详情
                  </Button>

                  <Button
                    size="small"
                    type={copiedKey === uriKey ? 'primary' : 'default'}
                    icon={copiedKey === uriKey ? <CheckOutlined /> : <CopyOutlined />}
                    style={{
                      borderRadius: 6,
                      fontSize: 12,
                      padding: '0 12px',
                      height: 28,
                    }}
                    onClick={() => handleCopy(item.uri, uriKey, `${displayTag} URI`)}
                  >
                    URI
                  </Button>

                  <Button
                    size="small"
                    type={copiedKey === clashKey ? 'primary' : 'default'}
                    icon={copiedKey === clashKey ? <CheckOutlined /> : <CopyOutlined />}
                    disabled={!item.clash}
                    style={{
                      borderRadius: 6,
                      fontSize: 12,
                      padding: '0 12px',
                      height: 28,
                    }}
                    onClick={() => handleCopy(item.clash, clashKey, `${displayTag} Clash`)}
                  >
                    Clash
                  </Button>

                  {!item.surge ? (
                    <Tooltip title="Surge 不支持该协议">
                      <Button
                        size="small"
                        disabled
                        icon={<CopyOutlined />}
                        style={{
                          borderRadius: 6,
                          fontSize: 12,
                          padding: '0 12px',
                          height: 28,
                        }}
                      >
                        Surge
                      </Button>
                    </Tooltip>
                  ) : (
                    <Button
                      size="small"
                      type={copiedKey === surgeKey ? 'primary' : 'default'}
                      icon={copiedKey === surgeKey ? <CheckOutlined /> : <CopyOutlined />}
                      style={{
                        borderRadius: 6,
                        fontSize: 12,
                        padding: '0 12px',
                        height: 28,
                      }}
                      onClick={() => handleCopy(item.surge, surgeKey, `${displayTag} Surge`)}
                    >
                      Surge
                    </Button>
                  )}
                </Space>
              </List.Item>
            )
          }}
        />
      )}

      <Modal
        title={`${detailItem?.tag || detailItem?.name || ''} 节点详情`}
        open={!!detailItem}
        onCancel={() => setDetailItem(null)}
        footer={<Button onClick={() => setDetailItem(null)}>关闭</Button>}
        width={620}
        destroyOnClose
      >
        {detailItem && (
          <Descriptions bordered size="small" column={1}>
            <Descriptions.Item label="节点地址 / IP">
              <Typography.Text copyable>{detailItem.server || '—'}</Typography.Text>
            </Descriptions.Item>
            <Descriptions.Item label="端口">
              <Typography.Text copyable>{String(detailItem.port)}</Typography.Text>
            </Descriptions.Item>
            <Descriptions.Item label="协议">
              <Tag color={PROTO_COLORS[detailItem.type] || 'default'}>{detailItem.type}</Tag>
            </Descriptions.Item>
            {Object.entries(detailItem.params || {})
              .filter(([key]) => !['服务器', '端口', '协议'].includes(key))
              .map(([key, value]) => (
                <Descriptions.Item key={key} label={key}>
                  <Typography.Text copyable={{ text: value }}>{value || '—'}</Typography.Text>
                </Descriptions.Item>
              ))}
          </Descriptions>
        )}
      </Modal>
    </div>
  )
}
