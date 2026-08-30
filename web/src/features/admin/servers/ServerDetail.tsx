import { useCallback, useEffect, useRef, useState } from 'react'
import type { PointerEvent as ReactPointerEvent } from 'react'
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
  EditOutlined,
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
  isCanceledRequest,
  listOutbounds,
  listRuleSets,
  listRules,
  remoteConfig,
  reorderRules,
  serverStatus,
  serviceAction,
  setConfigMode,
  setFinalOutbound,
  testOutbound,
  uninstallAgent,
  uninstallSingbox,
  updateAgent,
} from '../../../api'
import type { ImportSummary, NodeFormats, OutboundTest } from '../../../api'
import { applyWithToast, isConfigApplyResult, showConfigApplyResult } from '../../../configApply'
import type { Inbound, Outbound, RouteRule, RuleSet, Server } from '../../../types'
import InboundForm from './InboundForm'
import { OutboundForm, RuleForm } from './RoutingForms'
import { RuleSetsTab } from './RuleSetsTab'
import { ConfigEditorModal, ImportPreviewModal, InstallSingboxModal, NodeFormatsExportModal } from './ServerDialogs'
import { ServerOverviewCard } from './ServerOverviewCard'
import { copyToClipboard } from '../../../util'
import { RequestState } from '../../../components/RequestState'

// Matches the colored protocol labels used by node assignment and traffic.
const PROTOCOL_TAGS: Record<string, { label: string; color: string }> = {
  vless: { label: 'VLESS', color: 'blue' },
  vmess: { label: 'VMess', color: 'purple' },
  ss: { label: 'SS', color: 'green' },
  shadowsocks: { label: 'SS', color: 'green' },
  trojan: { label: 'Trojan', color: 'geekblue' },
  hysteria2: { label: 'Hysteria2', color: 'cyan' },
  hysteria: { label: 'Hysteria', color: 'cyan' },
  tuic: { label: 'TUIC', color: 'orange' },
  anytls: { label: 'AnyTLS', color: 'magenta' },
  socks: { label: 'SOCKS', color: 'default' },
  socks5: { label: 'SOCKS5', color: 'default' },
  snell: { label: 'Snell', color: 'gold' },
  mixed: { label: 'Mixed', color: 'volcano' },
  direct: { label: 'DIRECT', color: 'geekblue' },
}

function ProtocolTag({ type }: { type: string }) {
  const protocol = type.trim().toLowerCase()
  const tag = PROTOCOL_TAGS[protocol]
  return <Tag color={tag?.color}>{tag?.label || type}</Tag>
}

export default function ServerDetail() {
  const { id } = useParams()
  const sid = Number(id)
  const [server, setServer] = useState<Server | null>(null)
  const [pageLoading, setPageLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [sectionError, setSectionError] = useState<string | null>(null)
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
  const [cfgServerID, setCfgServerID] = useState<number | null>(null)

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
  const [testing, setTesting] = useState<Set<number>>(() => new Set())

  const loadAbortRef = useRef<AbortController | null>(null)
  const loadGenerationRef = useRef(0)
  const delayedLoadsRef = useRef<Set<number>>(new Set())
  const scopedControllersRef = useRef<Set<AbortController>>(new Set())
  const currentSIDRef = useRef(sid)
  currentSIDRef.current = sid

  const ruleOrderRef = useRef<RouteRule[]>([])
  const ruleOrderGenerationRef = useRef(0)
  const pendingRuleOrderRef = useRef<{ sid: number; order: number[] } | null>(null)
  const ruleReorderRunningRef = useRef(false)
  const ruleReorderRunnerRef = useRef(0)
  const draggedRuleIndexRef = useRef<number | null>(null)
  const pointerRuleDragRef = useRef<{ pointerId: number; sourceIndex: number } | null>(null)
  const [ruleDropIndex, setRuleDropIndex] = useState<number | null>(null)
  const [sortAnnouncement, setSortAnnouncement] = useState('')

  const scopedController = () => {
    const controller = new AbortController()
    scopedControllersRef.current.add(controller)
    return controller
  }

  const releaseScopedController = (controller: AbortController) => {
    scopedControllersRef.current.delete(controller)
  }

  const load = useCallback(async () => {
    const targetSID = sid
    if (currentSIDRef.current !== targetSID) return
    loadAbortRef.current?.abort()
    const controller = new AbortController()
    const generation = ++loadGenerationRef.current
    loadAbortRef.current = controller
    const ruleOrderGeneration = ruleOrderGenerationRef.current
    setPageLoading(true)
    setLoadError(null)
    try {
      const d = await getServer(targetSID, controller.signal)
      if (generation !== loadGenerationRef.current || currentSIDRef.current !== targetSID) return
      setServer(d.server)
      // 入站标签/端口等改动后，导出弹窗的缓存必须失效，下次打开时重新拉取。
      setFmtData(null)
      setInstall(d.install_command)
      setPublicURL(d.public_url)
      const [outboundRows, ruleRows, ruleSetRows] = await Promise.allSettled([
        listOutbounds(targetSID, controller.signal),
        listRules(targetSID, controller.signal),
        listRuleSets(targetSID, controller.signal),
      ])
      if (generation !== loadGenerationRef.current || currentSIDRef.current !== targetSID) return
      if (outboundRows.status === 'fulfilled') setOutbounds(outboundRows.value)
      if (ruleRows.status === 'fulfilled' && ruleOrderGeneration === ruleOrderGenerationRef.current) {
        ruleOrderRef.current = ruleRows.value
        setRules(ruleRows.value)
      }
      if (ruleSetRows.status === 'fulfilled') setRuleSets(ruleSetRows.value)
      const failures = [
        outboundRows.status === 'rejected' ? `出站：${errMsg(outboundRows.reason)}` : '',
        ruleRows.status === 'rejected' ? `规则：${errMsg(ruleRows.reason)}` : '',
        ruleSetRows.status === 'rejected' ? `规则集：${errMsg(ruleSetRows.reason)}` : '',
      ].filter(Boolean)
      setSectionError(failures.length > 0 ? failures.join('；') : null)
    } catch (error) {
      if (!isCanceledRequest(error) && generation === loadGenerationRef.current) setLoadError(errMsg(error))
      throw error
    } finally {
      if (generation === loadGenerationRef.current) {
        loadAbortRef.current = null
        setPageLoading(false)
      }
    }
  }, [sid])

  const scheduleLoad = useCallback((delay: number) => {
    const targetSID = sid
    const timer = window.setTimeout(() => {
      delayedLoadsRef.current.delete(timer)
      if (currentSIDRef.current !== targetSID) return
      void load().catch((error) => {
        if (!isCanceledRequest(error)) message.error(errMsg(error))
      })
    }, delay)
    delayedLoadsRef.current.add(timer)
  }, [load, sid])

  const [draggedRuleIndex, setDraggedRuleIndex] = useState<number | null>(null)

  const flushRuleReorder = useCallback(async () => {
    if (ruleReorderRunningRef.current) return
    ruleReorderRunningRef.current = true
    const runner = ++ruleReorderRunnerRef.current
    try {
      while (runner === ruleReorderRunnerRef.current && pendingRuleOrderRef.current) {
        const pending = pendingRuleOrderRef.current
        pendingRuleOrderRef.current = null
        if (currentSIDRef.current !== pending.sid) continue
        const result = await reorderRules(pending.sid, pending.order)
        if (currentSIDRef.current !== pending.sid) continue
        // Only the final queued order needs a toast; intermediate responses are
        // implementation details of a rapid sequence of drag operations.
        if (!pendingRuleOrderRef.current) showConfigApplyResult(result)
      }
    } catch (error) {
      if (runner !== ruleReorderRunnerRef.current) return
      pendingRuleOrderRef.current = null
      if (currentSIDRef.current === sid) {
        message.error(errMsg(error))
        void load().catch(() => {})
      }
    } finally {
      if (runner !== ruleReorderRunnerRef.current) return
      ruleReorderRunningRef.current = false
      if (pendingRuleOrderRef.current) void flushRuleReorder()
    }
  }, [load, sid])

  const commitRuleMove = useCallback((from: number, to: number) => {
    const current = ruleOrderRef.current
    if (from < 0 || to < 0 || from >= current.length || to >= current.length || from === to) return
    const next = [...current]
    const [moved] = next.splice(from, 1)
    next.splice(to, 0, moved)
    ruleOrderRef.current = next
    ruleOrderGenerationRef.current += 1
    setRules(next)
    setSortAnnouncement(`${moved.remark || moved.outbound || `规则 ${moved.id}`} 已移动到第 ${to + 1} 位`)
    pendingRuleOrderRef.current = { sid, order: next.map((rule) => rule.id) }
    void flushRuleReorder()
  }, [flushRuleReorder, sid])

  const handleRuleDrop = (dropIndex: number) => {
    const sourceIndex = draggedRuleIndexRef.current ?? draggedRuleIndex
    draggedRuleIndexRef.current = null
    setDraggedRuleIndex(null)
    if (sourceIndex === null) return
    commitRuleMove(sourceIndex, dropIndex)
  }

  const startRulePointerDrag = (event: ReactPointerEvent<HTMLButtonElement>, sourceIndex: number) => {
    if (server?.config_mode === 'raw') return
    if (event.pointerType !== 'touch' && event.pointerType !== 'pen') return
    event.preventDefault()
    event.stopPropagation()
    event.currentTarget.setPointerCapture(event.pointerId)
    pointerRuleDragRef.current = { pointerId: event.pointerId, sourceIndex }
    setDraggedRuleIndex(sourceIndex)
    setRuleDropIndex(sourceIndex)
  }

  const moveRulePointerDrag = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const drag = pointerRuleDragRef.current
    if (!drag || drag.pointerId !== event.pointerId) return
    event.preventDefault()
    const row = document.elementFromPoint(event.clientX, event.clientY)?.closest('tr[data-row-key]')
    const rowID = Number(row?.getAttribute('data-row-key'))
    const targetIndex = ruleOrderRef.current.findIndex((rule) => rule.id === rowID)
    if (targetIndex >= 0) setRuleDropIndex(targetIndex)
  }

  const finishRulePointerDrag = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const drag = pointerRuleDragRef.current
    if (!drag || drag.pointerId !== event.pointerId) return
    event.preventDefault()
    if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId)
    pointerRuleDragRef.current = null
    const targetIndex = ruleDropIndex
    setRuleDropIndex(null)
    setDraggedRuleIndex(null)
    commitRuleMove(drag.sourceIndex, targetIndex ?? drag.sourceIndex)
  }

  const cancelRulePointerDrag = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const drag = pointerRuleDragRef.current
    if (!drag || drag.pointerId !== event.pointerId) return
    if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId)
    pointerRuleDragRef.current = null
    setRuleDropIndex(null)
    setDraggedRuleIndex(null)
  }


  useEffect(() => {
    setServer(null)
    setOutbounds([])
    setRules([])
    ruleOrderRef.current = []
    setRuleSets([])
    setRefreshing(false)
    setCfgOpen(false)
    setCfgServerID(null)
    setCfgText('')
    setCfgLoading(false)
    setCfgSaving(false)
    setFmtOpen(false)
    setFmtData(null)
    setFmtLoading(false)
    setImportOpen(false)
    setImportSum(null)
    setImporting(false)
    setFormOpen(false)
    setEditing(null)
    setObOpen(false)
    setObEditing(null)
    setRuleOpen(false)
    setRuleEditing(null)
    setRsModalOpen(false)
    setBusy('')
    setTesting(new Set())
    setObTests({})
    void load().catch((error) => {
      if (!isCanceledRequest(error)) message.error(errMsg(error))
    })
    return () => {
      loadGenerationRef.current += 1
      loadAbortRef.current?.abort()
      for (const controller of scopedControllersRef.current) controller.abort()
      scopedControllersRef.current.clear()
      for (const timer of delayedLoadsRef.current) window.clearTimeout(timer)
      delayedLoadsRef.current.clear()
      pendingRuleOrderRef.current = null
      ruleReorderRunnerRef.current += 1
      ruleReorderRunningRef.current = false
      pointerRuleDragRef.current = null
      draggedRuleIndexRef.current = null
    }
  }, [load])

  const refresh = async () => {
    const targetSID = sid
    const controller = scopedController()
    setRefreshing(true)
    try {
      if (server?.online) await serverStatus(targetSID, controller.signal)
      if (currentSIDRef.current !== targetSID) return
      await load()
      if (currentSIDRef.current !== targetSID) return
      if (server?.online) message.success('已从 Agent 获取最新状态')
      else message.info('节点当前离线，已刷新面板状态')
    } catch (e) {
      if (controller.signal.aborted || currentSIDRef.current !== targetSID) return
      await load().catch(() => {})
      message.error(errMsg(e))
    } finally {
      releaseScopedController(controller)
      if (currentSIDRef.current === targetSID) setRefreshing(false)
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
    const targetSID = sid
    const controller = scopedController()
    setBusy('import')
    try {
      const r = await importConfig(targetSID, { dry_run: true }, controller.signal)
      if (currentSIDRef.current !== targetSID) return
      setImportSum(r.summary)
      setImportOpen(true)
    } catch (e) {
      if (!controller.signal.aborted && currentSIDRef.current === targetSID) message.error(errMsg(e))
    } finally {
      releaseScopedController(controller)
      if (currentSIDRef.current === targetSID) setBusy('')
    }
  }
  const doImport = async () => {
    const targetSID = sid
    if (currentSIDRef.current !== targetSID) return
    setImporting(true)
    try {
      await importConfig(targetSID, {})
      if (currentSIDRef.current !== targetSID) return
      message.success('已导入到面板')
      setImportOpen(false)
      void load().catch(() => {})
    } catch (e) {
      if (currentSIDRef.current === targetSID) message.error(errMsg(e))
    } finally {
      if (currentSIDRef.current === targetSID) setImporting(false)
    }
  }

  // Reachability is measured from the node, not from the panel.
  const runTest = async (o: Outbound) => {
    if (testing.has(o.id)) return
    const targetSID = sid
    const controller = scopedController()
    setTesting((prev) => new Set(prev).add(o.id))
    try {
      const r = await testOutbound(targetSID, o.id, controller.signal)
      if (currentSIDRef.current !== targetSID) return
      setObTests((prev) => ({ ...prev, [o.id]: r }))
      // Successful results stay in the fixed-width connectivity cell. This
      // avoids a second transient notification while the administrator tests
      // several outbounds in sequence.
      if (!r.ok) message.error(`${o.tag} 不通：${r.error || '连接失败'}`)
    } catch (e) {
      if (!controller.signal.aborted && currentSIDRef.current === targetSID) message.error(errMsg(e))
    } finally {
      releaseScopedController(controller)
      if (currentSIDRef.current === targetSID) {
        setTesting((prev) => {
          const next = new Set(prev)
          next.delete(o.id)
          return next
        })
      }
    }
  }

  // The install command is long and wraps badly inline, so it lives behind a
  // button and is shown (and copied) in a dialog instead.
  const showInstallCmd = (command = install) =>
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
              {command || '暂时无法生成安装命令，请刷新页面后重试。'}
            </pre>
            <Button
              size="small"
              icon={<CopyOutlined />}
              style={{ position: 'absolute', top: 8, right: 8 }}
              onClick={() =>
                copyToClipboard(command)
                  .then(() => message.success('已复制'))
                  .catch(() => message.error('复制失败，请手动选择命令'))
              }
              disabled={!command}
              title="复制安装命令"
              aria-label="复制安装命令"
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
      title: '同步 Agent?',
      okText: '开始同步',
      content: '面板将向在线节点下发当前提供的 Agent。程序会先完成校验，重启后若无法重新连接将自动恢复原版本。',
      onOk: async () => {
        const targetSID = sid
        if (currentSIDRef.current !== targetSID) return
        setBusy('agent')
        try {
          const result = await updateAgent(targetSID)
          if (currentSIDRef.current !== targetSID) return
          if (result.updated === false) {
            message.info(result.output || 'Agent 已是目标版本')
            return
          }
          message.success('Agent 已接收同步，正在重启并重新连接')
          scheduleLoad(4000)
        } catch (e) {
          if (currentSIDRef.current !== targetSID) return
          const text = errMsg(e)
          Modal.error({
            title: 'Agent 同步失败',
            width: 680,
            content: (
              <Typography.Paragraph copyable={{ text }} style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                {text}
              </Typography.Paragraph>
            ),
          })
        } finally {
          if (currentSIDRef.current === targetSID) setBusy('')
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
        const targetSID = sid
        if (currentSIDRef.current !== targetSID) return
        try {
          await uninstallAgent(targetSID)
          if (currentSIDRef.current !== targetSID) return
          message.success('Agent 正在卸载；面板节点和 sing-box 已保留')
          scheduleLoad(6000)
        } catch (e) {
          if (currentSIDRef.current !== targetSID) return
          message.error(errMsg(e))
          throw e
        }
      },
    })

  const confirmUninstallSingbox = () =>
    Modal.confirm({
      title: '卸载 Sing-box?',
      okText: '确认卸载 Sing-box',
      okType: 'danger',
      content: (
        <Alert
          type="warning"
          showIcon
          message="将停止服务并删除 Sing-box 程序。"
          description="Agent、/etc/sing-box 配置目录和面板节点数据都会保留，之后可直接重新安装。"
        />
      ),
      onOk: async () => {
        const targetSID = sid
        if (currentSIDRef.current !== targetSID) return
        setBusy('uninstall-singbox')
        try {
          await uninstallSingbox(targetSID)
          if (currentSIDRef.current !== targetSID) return
          setServer((current) => current ? {
            ...current,
            singbox_installed: false,
            singbox_active: false,
            singbox_version: '',
          } : current)
          message.success('Sing-box 已卸载，配置和面板数据已保留')
        } catch (e) {
          if (currentSIDRef.current !== targetSID) return
          message.error(errMsg(e))
          throw e
        } finally {
          if (currentSIDRef.current === targetSID) setBusy('')
        }
      },
    })

  const openNodeFormats = async () => {
    const targetSID = sid
    const controller = scopedController()
    setFmtOpen(true)
    if (fmtData) {
      releaseScopedController(controller)
      return
    }
    setFmtLoading(true)
    try {
      const data = await getNodeFormats(targetSID, controller.signal)
      if (currentSIDRef.current !== targetSID) return
      setFmtData(data)
    } catch (e) {
      if (!controller.signal.aborted && currentSIDRef.current === targetSID) {
        message.error(errMsg(e))
        setFmtOpen(false)
      }
    } finally {
      releaseScopedController(controller)
      if (currentSIDRef.current === targetSID) setFmtLoading(false)
    }
  }

  const onDeleteInbound = (ib: Inbound) => {
    const targetSID = sid
    return Modal.confirm({
      title: `删除入站 ${ib.tag}?`,
      okType: 'danger',
      okText: '确定',
      cancelText: '取消',
      centered: true,
      // Close the confirm immediately; the delete runs in the background behind
      // the "配置下发中" toast, matching the create/update flow.
      onOk: () => {
        if (currentSIDRef.current !== targetSID) return
        applyWithToast(`inbound-del-${ib.id}`, () => deleteInbound(targetSID, ib.id)).then(loadCurrentServer)
      },
    })
  }

  const onDeleteOutbound = (o: Outbound) => {
    const targetSID = sid
    return Modal.confirm({
      title: `删除出站 ${o.tag}?`,
      okType: 'danger',
      okText: '确定',
      cancelText: '取消',
      centered: true,
      onOk: () => {
        if (currentSIDRef.current !== targetSID) return
        applyWithToast(`outbound-del-${o.id}`, () => deleteOutbound(targetSID, o.id)).then(loadCurrentServer)
      },
    })
  }

  const onDeleteRule = (r: RouteRule) => {
    const targetSID = sid
    return Modal.confirm({
      title: `确认删除该路由规则?`,
      okType: 'danger',
      okText: '确定',
      cancelText: '取消',
      centered: true,
      onOk: () => {
        if (currentSIDRef.current !== targetSID) return
        applyWithToast(`rule-del-${r.id}`, () => deleteRule(targetSID, r.id)).then(loadCurrentServer)
      },
    })
  }

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
    const targetSID = sid
    if (currentSIDRef.current !== targetSID) return
    setBusy(label)
    try {
      const result = await fn()
      if (currentSIDRef.current !== targetSID) return
      if (isConfigApplyResult(result)) showConfigApplyResult(result)
      else message.success(ok)
      void load().catch(() => {})
    } catch (e) {
      if (currentSIDRef.current === targetSID) message.error(errMsg(e))
    } finally {
      if (currentSIDRef.current === targetSID) setBusy('')
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
    const targetSID = sid
    const controller = scopedController()
    setCfgServerID(targetSID)
    setCfgOpen(true)
    setCfgLoading(true)
    setCfgText('')
    try {
      const cfg = await remoteConfig(targetSID, controller.signal)
      if (currentSIDRef.current !== targetSID) return
      let text = cfg.raw || ''
      try {
        text = JSON.stringify(JSON.parse(text), null, 2)
      } catch {
        // leave raw text as-is if it isn't valid JSON
      }
      setCfgText(cfg.exists ? text : '')
    } catch (e) {
      if (!controller.signal.aborted && currentSIDRef.current === targetSID) message.error(errMsg(e))
    } finally {
      releaseScopedController(controller)
      if (currentSIDRef.current === targetSID) setCfgLoading(false)
    }
  }

  const saveConfig = async () => {
    if (cfgSaving) return
    const targetSID = cfgServerID
    if (!targetSID || targetSID !== sid || currentSIDRef.current !== targetSID) {
      message.error('服务器已切换，请重新打开配置编辑器')
      setCfgOpen(false)
      setCfgServerID(null)
      return
    }
    setCfgSaving(true)
    try {
      const result = await applyRawConfig(targetSID, cfgText)
      if (currentSIDRef.current !== targetSID) return
      if (result.config_mode === 'managed') {
        message.success('已校验、下发并自动进入面板管理')
      } else if (result.summary.skipped?.length) {
        message.warning(`已下发并同步；${result.summary.skipped.length} 项无法转换为面板表单，已在原始配置中完整保留`)
      } else {
        message.info('已下发并同步；配置无法无损转换，继续保留完整原始配置')
      }
      setCfgOpen(false)
      setCfgServerID(null)
      void load().catch(() => {})
    } catch (e) {
      if (currentSIDRef.current === targetSID) message.error(errMsg(e))
    } finally {
      if (currentSIDRef.current === targetSID) setCfgSaving(false)
    }
  }

  const loadCurrentServer = () => {
    if (currentSIDRef.current !== sid) return
    void load().catch((error) => {
      if (!isCanceledRequest(error)) message.error(errMsg(error))
    })
  }



  if (!server) {
    return <RequestState loading={pageLoading} error={loadError} hasData={false} onRetry={loadCurrentServer}><span /></RequestState>
  }
  const rawMode = server.config_mode === 'raw'
  return (
    <RequestState loading={pageLoading} error={loadError || sectionError} hasData onRetry={loadCurrentServer}>
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <ServerOverviewCard
        server={server}
        publicURL={publicURL}
        busy={busy}
        refreshing={refreshing}
        exportLoading={fmtLoading}
        onRefresh={refresh}
                  onInstallSingbox={() => setInstallOpen(true)}
                  onUninstallSingbox={confirmUninstallSingbox}
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
            { title: '协议', dataIndex: 'type', render: (v: string) => <ProtocolTag type={v} /> },
            { title: '端口', dataIndex: 'listen_port' },
            {
              title: '用户模式',
              render: (_, ib: Inbound) => ib.settings?.multi_user
                ? <Tag color="blue">多用户</Tag>
                : <Tag>单用户</Tag>,
            },
            {
              title: '启用',
              dataIndex: 'enabled',
              render: (v: boolean) => (v ? <Tag color="green">是</Tag> : <Tag>否</Tag>),
            },
            {
              title: '操作',
              render: (_, ib: Inbound) => (
                <Space>
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
          tableLayout="fixed"
          scroll={{ x: 780 }}
          dataSource={outbounds}
          columns={[
            { title: '标签', dataIndex: 'tag', width: 150, ellipsis: true },
            { title: '类型', dataIndex: 'type', width: 130, render: (v: string) => <ProtocolTag type={v} /> },
            { title: '目标地址', width: 240, ellipsis: true, render: (_, o: Outbound) => `${o.settings?.server || '—'}:${o.settings?.server_port || ''}` },
            {
              title: '连通性',
              width: 170,
              render: (_, o: Outbound) => {
                const t = obTests[o.id]
                const isTesting = testing.has(o.id)
                return (
                  <Space size={4} style={{ whiteSpace: 'nowrap' }}>
                    <span style={{ display: 'inline-flex', minWidth: 88 }}>
                      {isTesting
                        ? <span style={{ color: '#4096ff' }}>测试中…</span>
                        : !t
                          ? <span style={{ color: '#bbb' }}>未测试</span>
                          : t.ok
                            ? <Tag color="green">通 · {t.latency_ms}ms</Tag>
                            : <Tag color="red" title={t.error}>不通</Tag>}
                    </span>
                    <Button
                      size="small"
                      type="text"
                      icon={<ReloadOutlined />}
                      loading={isTesting}
                      onClick={() => void runTest(o)}
                      title="测试连通性"
                      aria-label={`测试 ${o.tag} 连通性`}
                      style={{ width: 28, minWidth: 28, paddingInline: 0 }}
                    />
                  </Space>
                )
              },
            },
            {
              title: '操作',
              width: 110,
              render: (_, o: Outbound) => (
                <Space>
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
              draggedRuleIndexRef.current = index!
              setDraggedRuleIndex(index!)
              e.dataTransfer.effectAllowed = 'move'
              e.dataTransfer.setData('text/plain', String(index))
            },
            onDragOver: (e) => e.preventDefault(),
            onDragEnd: () => {
              draggedRuleIndexRef.current = null
              setDraggedRuleIndex(null)
            },
            onDrop: () => handleRuleDrop(index!),
            style: {
              cursor: 'grab',
              outline: ruleDropIndex === index ? '2px solid #91caff' : undefined,
              outlineOffset: -2,
            },
          })}
          columns={[
            {
              title: '排序',
              width: 70,
              align: 'center',
              render: (_, __, index) => (
                <button
                  type="button"
                  className="rule-drag-handle"
                  aria-label={`调整第 ${index + 1} 条规则顺序`}
                  aria-describedby="rule-sort-help"
                  title="按住拖拽以调整规则顺序"
                  disabled={rawMode}
                  onKeyDown={(event) => {
                    if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
                      event.preventDefault()
                      commitRuleMove(index, index + (event.key === 'ArrowUp' ? -1 : 1))
                    }
                  }}
                  onPointerDown={(event) => startRulePointerDrag(event, index)}
                  onPointerMove={moveRulePointerDrag}
                  onPointerUp={finishRulePointerDrag}
                  onPointerCancel={cancelRulePointerDrag}
                >
                  <MenuOutlined style={{ color: '#94a3b8', fontSize: 14, cursor: 'grab' }} />
                </button>
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
          key={sid}
          serverId={sid}
          ruleSets={ruleSets}
          outbounds={outbounds}
          onRefresh={loadCurrentServer}
          rawMode={rawMode}
        />
      </Modal>

      <InboundForm
        key={`inbound-${sid}`}
        serverId={sid}
        inbound={editing}
        open={formOpen}
        onClose={() => setFormOpen(false)}
        onSaved={loadCurrentServer}
      />

      <OutboundForm
        key={`outbound-${sid}`}
        serverId={sid}
        outbound={obEditing}
        open={obOpen}
        onClose={() => setObOpen(false)}
        onSaved={loadCurrentServer}
      />

      <RuleForm
        key={`rule-${sid}`}
        serverId={sid}
        rule={ruleEditing}
        outboundTags={outboundTags}
        inboundTags={inboundTags}
        ruleSets={ruleSets}
        open={ruleOpen}
        onClose={() => setRuleOpen(false)}
        onSaved={loadCurrentServer}
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
        onCancel={() => {
          setCfgOpen(false)
          setCfgServerID(null)
        }}
        onSave={saveConfig}
      />

      <InstallSingboxModal
        open={installOpen}
        form={installForm}
        onCancel={() => setInstallOpen(false)}
        onConfirm={doInstall}
      />

      <NodeFormatsExportModal open={fmtOpen} loading={fmtLoading} data={fmtData} onCancel={() => setFmtOpen(false)} />

      <span id="rule-sort-help" className="sr-only">使用上方向键或下方向键调整规则顺序</span>
      <div className="sr-only" aria-live="polite" aria-atomic="true">{sortAnnouncement}</div>

    </Space>
    </RequestState>
  )
}
