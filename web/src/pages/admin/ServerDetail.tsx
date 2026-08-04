import { useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Form,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Typography,
  message,
} from 'antd'
import {
  ControlOutlined,
  CopyOutlined,
  DeleteOutlined,
  EditOutlined,
  LockOutlined,
  MenuOutlined,
  PlusOutlined,
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
import { applyWithToast, isConfigApplyResult, showConfigApplyResult } from '../../configApply'
import type { Inbound, Outbound, RouteRule, RuleSet, Server } from '../../types'
import InboundForm from './InboundForm'
import { OutboundForm, RuleForm } from './RoutingForms'
import { RuleSetsTab } from './RuleSetsTab'
import { ConfigEditorModal, ImportPreviewModal, InstallSingboxModal, NodeFormatsExportModal } from './ServerDialogs'
import { ServerOverviewCard } from './ServerOverviewCard'

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
    // 入站标签/端口等改动后，导出弹窗的缓存必须失效，下次打开时重新拉取。
    setFmtData(null)
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

  const confirmUninstallAgent = () =>
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

  const openNodeFormats = async () => {
    setFmtOpen(true)
    if (fmtData) return
    setFmtLoading(true)
    try {
      setFmtData(await getNodeFormats(sid))
    } catch (e) {
      message.error(errMsg(e))
      setFmtOpen(false)
    } finally {
      setFmtLoading(false)
    }
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
      // Close the confirm immediately; the delete runs in the background behind
      // the "配置下发中" toast, matching the create/update flow.
      onOk: () => {
        applyWithToast(`inbound-del-${ib.id}`, () => deleteInbound(sid, ib.id)).then(load)
      },
    })

  const onDeleteOutbound = (o: Outbound) =>
    Modal.confirm({
      title: `删除出站 ${o.tag}?`,
      okType: 'danger',
      okText: '确定',
      cancelText: '取消',
      centered: true,
      onOk: () => {
        applyWithToast(`outbound-del-${o.id}`, () => deleteOutbound(sid, o.id)).then(load)
      },
    })

  const onDeleteRule = (r: RouteRule) =>
    Modal.confirm({
      title: `确认删除该路由规则?`,
      okType: 'danger',
      okText: '确定',
      cancelText: '取消',
      centered: true,
      onOk: () => {
        applyWithToast(`rule-del-${r.id}`, () => deleteRule(sid, r.id)).then(load)
      },
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
      const result = await applyRawConfig(sid, cfgText)
      if (result.config_mode === 'managed') {
        message.success('已校验、下发并自动进入面板管理')
      } else if (result.summary.skipped?.length) {
        message.warning(`已下发并同步；${result.summary.skipped.length} 项无法转换为面板表单，已在原始配置中完整保留`)
      } else {
        message.info('已下发并同步；配置无法无损转换，继续保留完整原始配置')
      }
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
      <ServerOverviewCard
        server={server}
        publicURL={publicURL}
        busy={busy}
        refreshing={refreshing}
        exportLoading={fmtLoading}
        onRefresh={refresh}
                  onInstallSingbox={() => setInstallOpen(true)}
                  onInstallAgent={installOrUpgradeAgent}
                  onUninstallAgent={confirmUninstallAgent}
                  onImport={openImport}
                  onEditConfig={openConfigEditor}
                  onRestart={() => run('restart', () => serviceAction(sid, 'restart'), 'sing-box 已重启')}
                  onExport={openNodeFormats}
                />

      {rawMode && (
        <Alert
          type="warning"
          showIcon
          message="该节点当前使用原始配置模式"
          description="配置中存在面板无法无损转换的内容，完整 config.json 仍是当前生效配置。可识别的节点已同步；强制切换会舍弃扩展字段。"
          action={<Button danger loading={busy === 'mode'} onClick={switchToManaged}>舍弃扩展字段并切换</Button>}
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
              title: '用户模式',
              render: (_, ib: Inbound) => ib.settings?.multi_user
                ? <Tag color="blue">独立凭证</Tag>
                : <Tag>单凭证</Tag>,
            },
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
                    {m.domain?.map((domain) => (
                      <Tag key={`domain-${domain}`} style={{ background: '#fff7e6', color: '#d46b08', borderColor: '#ffd591' }}>
                        域名: {domain}
                      </Tag>
                    ))}
                    {m.domain_suffix?.map((ds) => (
                      <Tag key={`suffix-${ds}`} style={{ background: '#fff7e6', color: '#d46b08', borderColor: '#ffd591' }}>
                        域名: {ds}
                      </Tag>
                    ))}
                    {m.domain_keyword?.map((keyword) => (
                      <Tag key={`keyword-${keyword}`} style={{ background: '#fff7e6', color: '#d46b08', borderColor: '#ffd591' }}>
                        关键词: {keyword}
                      </Tag>
                    ))}
                    {m.ip_cidr?.map((ip) => (
                      <Tag key={`ip-${ip}`} style={{ background: '#fff0f6', color: '#c41d7f', borderColor: '#ffadd2' }}>
                        目标 IP: {ip}
                      </Tag>
                    ))}
                    {m.source_ip_cidr?.map((ip) => (
                      <Tag key={`source-ip-${ip}`} style={{ background: '#fff0f6', color: '#c41d7f', borderColor: '#ffadd2' }}>
                        来源 IP: {ip}
                      </Tag>
                    ))}
                    {m.port?.map((port) => (
                      <Tag key={`port-${port}`} style={{ background: '#e6fffb', color: '#08979c', borderColor: '#87e8de' }}>
                        端口: {port}
                      </Tag>
                    ))}
                    {m.protocol?.map((p) => (
                      <Tag key={`protocol-${p}`} style={{ background: '#f9f0ff', color: '#722ed1', borderColor: '#d3ade6' }}>
                        协议: {p}
                      </Tag>
                    ))}
                    {m.network ? (
                      <Tag style={{ background: '#f0f5ff', color: '#2f54eb', borderColor: '#adc6ff' }}>
                        网络: {m.network.toUpperCase()}
                      </Tag>
                    ) : null}
                    {!m.rule_set?.length && !m.inbound?.length && !m.domain?.length && !m.domain_suffix?.length && !m.domain_keyword?.length && !m.ip_cidr?.length && !m.source_ip_cidr?.length && !m.port?.length && !m.protocol?.length && !m.network && (
                      <Tag style={{ background: '#f5f5f5', color: '#595959', borderColor: '#d9d9d9' }}>
                        匹配全部流量
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

      <ImportPreviewModal
        open={importOpen}
        loading={importing}
        summary={importSum}
        onCancel={() => setImportOpen(false)}
        onConfirm={doImport}
      />

      <ConfigEditorModal
        open={cfgOpen}
        loading={cfgLoading}
        saving={cfgSaving}
        text={cfgText}
        onText={setCfgText}
        onCancel={() => setCfgOpen(false)}
        onSave={saveConfig}
      />

      <InstallSingboxModal
        open={installOpen}
        form={installForm}
        onCancel={() => setInstallOpen(false)}
        onConfirm={doInstall}
      />

      <NodeFormatsExportModal open={fmtOpen} loading={fmtLoading} data={fmtData} onCancel={() => setFmtOpen(false)} />

    </Space>
  )
}
