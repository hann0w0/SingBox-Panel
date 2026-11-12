import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import type { PointerEvent as ReactPointerEvent, ReactNode } from 'react'
import {
  Alert,
  Button,
  Form,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Table,
  Typography,
  message,
} from '../../../ui'
import {
  Copy,
  Trash2,
  Pencil,
  GripVertical,
  Plus,
  RotateCw,
} from 'lucide-react'
import { useNavigate, useParams } from 'react-router-dom'
import {
  applyRawConfig,
  deleteInbound,
  deleteOutbound,
  deleteRule,
  errMsg,
  getServer,
  importConfig,
  installSingbox,
  isCanceledRequest,
  listOutbounds,
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
import type { ImportSummary, OutboundTest } from '../../../api'
import { applyWithToast, isConfigApplyResult, showConfigApplyResult } from '../../../configApply'
import type { Inbound, Outbound, RouteRule, Server } from '../../../types'
import InboundForm from './InboundForm'
import { OutboundForm, RuleForm } from './RoutingForms'
import { ConfigEditorModal, ImportPreviewModal, InstallSingboxModal } from './ServerDialogs'
import { NodeFormatsExportModal } from './NodeFormatsModal'
import { ServerOverviewCard } from './ServerOverviewCard'
import ServerTraffic from './ServerTraffic'
import Logs from '../logs/Logs'
import { copyToClipboard, formatBytes } from '../../../util'
import { Pills, Stats, Status } from '../../../ui/Page'
import '../../../styles/server-detail.css'
import { RequestState } from '../../../components/RequestState'
import { MODAL_MD } from '../../../ui/modal'

const PROTOCOL_LABELS: Record<string, string> = {
  vless: 'VLESS', vmess: 'VMess', ss: 'Shadowsocks', shadowsocks: 'Shadowsocks', trojan: 'Trojan',
  hysteria2: 'Hysteria2', hysteria: 'Hysteria', tuic: 'TUIC', anytls: 'AnyTLS', socks: 'SOCKS',
  socks5: 'SOCKS5', snell: 'Snell', mixed: 'Mixed', direct: 'Direct',
}

function ProtocolTag({ type }: { type: string }) {
  return <span className="ui-tag">{PROTOCOL_LABELS[type.trim().toLowerCase()] || type}</span>
}

const DETAIL_SECTIONS = ['inbounds', 'outbounds', 'rules', 'traffic', 'logs'] as const
type DetailSection = typeof DETAIL_SECTIONS[number]

export default function ServerDetail() {
  const { id, section: sectionParam } = useParams()
  const navigate = useNavigate()
  const section: DetailSection = (DETAIL_SECTIONS as readonly string[]).includes(sectionParam || '') ? sectionParam as DetailSection : 'inbounds'
  const sid = Number(id)
  const [server, setServer] = useState<Server | null>(null)
  const [pageLoading, setPageLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [sectionError, setSectionError] = useState<string | null>(null)
  const [install, setInstall] = useState('')
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
  const [cfgSessionID, setCfgSessionID] = useState(0)
  const cfgSessionRef = useRef<{
    serverID: number
    request: AbortController | null
    saving: boolean
  } | null>(null)

  // 节点格式弹窗状态
  const [fmtOpen, setFmtOpen] = useState(false)

  const [outbounds, setOutbounds] = useState<Outbound[]>([])
  const [rules, setRules] = useState<RouteRule[]>([])
  const [obOpen, setObOpen] = useState(false)
  const [obEditing, setObEditing] = useState<Outbound | null>(null)
  const [ruleOpen, setRuleOpen] = useState(false)
  const [ruleEditing, setRuleEditing] = useState<RouteRule | null>(null)
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

  const invalidateConfigEditor = useCallback(() => {
    const session = cfgSessionRef.current
    cfgSessionRef.current = null
    session?.request?.abort()
  }, [])

  useLayoutEffect(() => invalidateConfigEditor, [sid, invalidateConfigEditor])

  const closeConfigEditor = () => {
    invalidateConfigEditor()
    setCfgOpen(false)
    setCfgLoading(false)
    setCfgSaving(false)
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
      setInstall(d.install_command)
      const [outboundRows, ruleRows] = await Promise.allSettled([
        listOutbounds(targetSID, controller.signal),
        listRules(targetSID, controller.signal),
      ])
      if (generation !== loadGenerationRef.current || currentSIDRef.current !== targetSID) return
      if (outboundRows.status === 'fulfilled') setOutbounds(outboundRows.value)
      if (ruleRows.status === 'fulfilled' && ruleOrderGeneration === ruleOrderGenerationRef.current) {
        ruleOrderRef.current = ruleRows.value
        setRules(ruleRows.value)
      }
      const failures = [
        outboundRows.status === 'rejected' ? `出站：${errMsg(outboundRows.reason)}` : '',
        ruleRows.status === 'rejected' ? `规则：${errMsg(ruleRows.reason)}` : '',
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
    setRefreshing(false)
    setCfgOpen(false)
    setCfgText('')
    setCfgLoading(false)
    setCfgSaving(false)
    setFmtOpen(false)
    setImportOpen(false)
    setImportSum(null)
    setImporting(false)
    setFormOpen(false)
    setEditing(null)
    setObOpen(false)
    setObEditing(null)
    setRuleOpen(false)
    setRuleEditing(null)
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
      content: '面板无法识别的字段将被舍弃。',
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
      if (r.inconclusive) {
        message.info(`${o.tag}：${r.note || '无法直接探测端口'}`)
      } else if (!r.ok) {
        message.error(`${o.tag} 不通：${r.error || '连接失败'}`)
      }
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
      width: MODAL_MD,
      content: (
        <div>
          
          <div style={{ position: 'relative' }}>
            <pre
              style={{
                background: 'var(--console-surface)',
                border: '1px solid var(--console-border)',
                borderRadius: 6,
                padding: '12px 14px',
                fontSize: 12,
                lineHeight: 1.75,
                color: 'var(--console-ink)',
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
              icon={<Copy />}
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
            width: MODAL_MD,
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
      content: '面板中的配置会保留。',
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
      content: '配置目录和面板数据会保留。',
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

  const onDeleteInbound = (ib: Inbound) => {
    const targetSID = sid
    return Modal.confirm({
      title: '删除入站',
      content: `确认删除「${ib.tag}」？`,
      okType: 'danger',
      okText: '删除',
      cancelText: '取消',
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
      title: '删除出站',
      content: `确认删除「${o.tag}」？`,
      okType: 'danger',
      okText: '删除',
      cancelText: '取消',
      onOk: () => {
        if (currentSIDRef.current !== targetSID) return
        applyWithToast(`outbound-del-${o.id}`, () => deleteOutbound(targetSID, o.id)).then(loadCurrentServer)
      },
    })
  }

  const onDeleteRule = (r: RouteRule) => {
    const targetSID = sid
    return Modal.confirm({
      title: '删除路由规则',
      content: '确认删除该路由规则？',
      okType: 'danger',
      okText: '删除',
      cancelText: '取消',
      onOk: () => {
        if (currentSIDRef.current !== targetSID) return
        applyWithToast(`rule-del-${r.id}`, () => deleteRule(targetSID, r.id)).then(loadCurrentServer)
      },
    })
  }

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
      Modal.info({ title: '安装输出', width: MODAL_MD, content: <pre style={{ whiteSpace: 'pre-wrap' }}>{r.output || 'ok'}</pre> })
    }, '安装完成')
  }

  const openConfigEditor = async () => {
    const targetSID = sid
    if (currentSIDRef.current !== targetSID) return
    invalidateConfigEditor()
    const controller = new AbortController()
    const session = { serverID: targetSID, request: controller as AbortController | null, saving: false }
    cfgSessionRef.current = session
    const isCurrentSession = () => cfgSessionRef.current === session && currentSIDRef.current === targetSID
    // Each opening gets a new Modal instance, including its internal submit lock.
    setCfgSessionID((value) => value + 1)
    setCfgOpen(true)
    setCfgLoading(true)
    setCfgSaving(false)
    setCfgText('')
    try {
      const cfg = await remoteConfig(targetSID, controller.signal)
      if (!isCurrentSession()) return
      let text = cfg.raw || ''
      try {
        text = JSON.stringify(JSON.parse(text), null, 2)
      } catch {
        // leave raw text as-is if it isn't valid JSON
      }
      setCfgText(cfg.exists ? text : '')
    } catch (e) {
      if (isCurrentSession() && !isCanceledRequest(e)) message.error(errMsg(e))
    } finally {
      if (isCurrentSession()) {
        session.request = null
        setCfgLoading(false)
      }
    }
  }

  const saveConfig = async () => {
    const session = cfgSessionRef.current
    if (!session || session.saving || session.request) return
    const targetSID = session.serverID
    if (targetSID !== sid || currentSIDRef.current !== targetSID) {
      message.error('服务器已切换，请重新打开配置编辑器')
      closeConfigEditor()
      return
    }
    const isCurrentSession = () => cfgSessionRef.current === session && currentSIDRef.current === targetSID
    session.saving = true
    setCfgSaving(true)
    try {
      const result = await applyRawConfig(targetSID, cfgText)
      if (!isCurrentSession()) return
      if (result.config_mode === 'managed') {
        message.success('已校验、下发并自动进入面板管理')
      } else if (result.summary.skipped?.length) {
        message.warning(`已下发并同步；${result.summary.skipped.length} 项无法转换为面板表单，已在原始配置中完整保留`)
      } else {
        message.info('已下发并同步；配置无法无损转换，继续保留完整原始配置')
      }
      for (const warning of result.warnings ?? []) {
        message.warning({ content: `安全提醒：${warning}`, duration: 10 })
      }
      closeConfigEditor()
      void load().catch(() => {})
    } catch (e) {
      if (isCurrentSession()) message.error(errMsg(e))
    } finally {
      if (isCurrentSession()) {
        session.saving = false
        setCfgSaving(false)
      }
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
  const finalOutbound = server.final_outbound || 'direct'
  const memPct = server.mem_total ? Math.round((server.mem_used / server.mem_total) * 100) : null
  const goSection = (key: string) => navigate(key === 'inbounds' ? `/admin/servers/${sid}` : `/admin/servers/${sid}/${key}`, { replace: true })
  const addButton: Partial<Record<DetailSection, ReactNode>> = {
    inbounds: <Button type="primary" size="small" icon={<Plus />} disabled={rawMode} onClick={() => { setEditing(null); setFormOpen(true) }}>新增入站</Button>,
    outbounds: <Button type="primary" size="small" icon={<Plus />} disabled={rawMode} onClick={() => { setObEditing(null); setObOpen(true) }}>新增出站</Button>,
    rules: <Button type="primary" size="small" icon={<Plus />} disabled={rawMode} onClick={() => { setRuleEditing(null); setRuleOpen(true) }}>新增规则</Button>,
  }
  const rowActions = (onEdit: () => void, onDelete: () => void, label: string) => (
    <span className="ui-row-actions">
      <Button size="small" type="text" icon={<Pencil />} aria-label={`编辑 ${label}`} disabled={rawMode} onClick={onEdit} />
      <Button size="small" type="text" icon={<Trash2 />} aria-label={`删除 ${label}`} disabled={rawMode} onClick={onDelete} />
    </span>
  )

  const inboundTable = (
    <Table
      rowKey="id"
      scroll={{ x: 560 }}
      dataSource={server.inbounds || []}
      locale={{ emptyText: '暂无入站' }}
      rowClassName={(ib: Inbound) => (ib.enabled === false ? 'is-disabled' : '')}
      columns={[
        { title: '标签', dataIndex: 'tag', render: (v: string) => <span className="sd-strong">{v}</span> },
        { title: '协议', dataIndex: 'type', render: (v: string) => <ProtocolTag type={v} /> },
        { title: '端口', dataIndex: 'listen_port', render: (v: number) => <span className="mono">{v}</span> },
        { title: '用户', render: (_, ib: Inbound) => <span className="muted">{ib.settings?.multi_user ? '多用户' : '单用户'}</span> },
        { title: '', width: 88, align: 'right', render: (_, ib: Inbound) => rowActions(() => { setEditing(ib); setFormOpen(true) }, () => onDeleteInbound(ib), `入站 ${ib.tag}`) },
      ]}
    />
  )

  const outboundTable = (
    <Table
      rowKey="id"
      tableLayout="fixed"
      scroll={{ x: 720 }}
      dataSource={outbounds}
      locale={{ emptyText: '暂无出站' }}
      columns={[
        { title: '标签', dataIndex: 'tag', width: 160, ellipsis: true, render: (v: string) => <span className="sd-strong">{v}</span> },
        { title: '类型', dataIndex: 'type', width: 120, render: (v: string) => <ProtocolTag type={v} /> },
        { title: '地址', width: 240, ellipsis: true, render: (_, o: Outbound) => <span className="mono">{`${o.settings?.server || '—'}:${o.settings?.server_port || ''}`}</span> },
        {
          title: '连通性',
          width: 150,
          render: (_, o: Outbound) => {
            const t = obTests[o.id]
            const isTesting = testing.has(o.id)
            return (
              <span className="sd-test">
                <span className="sd-test-result">
                  {isTesting
                    ? <span className="faint">测试中…</span>
                    : !t
                      ? <span className="faint">—</span>
                      : t.ok
                        ? <Status tone="ok">{t.latency_ms} ms</Status>
                        : t.inconclusive
                          ? <span title={t.note}><Status tone="warn">仅解析</Status></span>
                          : <span title={t.error}><Status tone="error">不通</Status></span>}
                </span>
                <Button size="small" type="text" icon={<RotateCw />} loading={isTesting} onClick={() => void runTest(o)} aria-label={`测试 ${o.tag} 连通性`} />
              </span>
            )
          },
        },
        { title: '', width: 88, align: 'right', render: (_, o: Outbound) => rowActions(() => { setObEditing(o); setObOpen(true) }, () => onDeleteOutbound(o), `出站 ${o.tag}`) },
      ]}
    />
  )

  const matchChips = (r: RouteRule) => {
    const m = r.match || {}
    const groups: [string, (string | number)[] | undefined][] = [
      [m.rule_set_ip_cidr_match_source ? '来源 IP 规则集' : '规则集', m.rule_set], ['入站', m.inbound], ['域名', m.domain], ['后缀', m.domain_suffix],
      ['关键词', m.domain_keyword], ['目标 IP', m.ip_cidr], ['来源 IP', m.source_ip_cidr],
      ['端口', m.port], ['协议', m.protocol], ['网络', m.network ? [m.network.toUpperCase()] : undefined],
    ]
    const chips = groups.flatMap(([label, values]) => (values || []).map((value) => (
      <span key={`${label}-${value}`} className="sd-chip"><span>{label}</span>{String(value)}</span>
    )))
    return chips.length ? chips : [<span key="all" className="faint">全部流量</span>]
  }
  const ruleTarget = (r: RouteRule) => {
    const v = r.outbound
    const act = (r.match?.action as string) || (v === 'block' ? 'reject' : v)
    if (act === 'reject' || v === 'block' || v === 'reject') return r.match?.method === 'drop' ? '静默丢弃' : '快速拒绝'
    if (act === 'sniff' || v === 'sniff') return 'sniff'
    if (act === 'hijack-dns' || v === 'hijack-dns') return 'hijack-dns'
    return v
  }

  const rulesTable = (
    <>
      <Table
        rowKey="id"
        scroll={{ x: 640 }}
        dataSource={rules}
        locale={{ emptyText: '暂无规则' }}
        rowClassName={(r: RouteRule) => (r.enabled ? '' : 'is-disabled')}
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
            outline: ruleDropIndex === index ? '2px solid var(--console-focus)' : undefined,
            outlineOffset: -2,
          },
        })}
        columns={[
          {
            title: '',
            width: 44,
            align: 'center',
            render: (_, __, index) => (
              <button
                type="button"
                className="rule-drag-handle sd-drag"
                aria-label={`调整第 ${index + 1} 条规则顺序`}
                aria-describedby="rule-sort-help"
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
                <GripVertical />
              </button>
            ),
          },
          { title: '匹配', render: (_, r: RouteRule) => <span className="sd-chips">{matchChips(r)}</span> },
          { title: '出站', width: 160, render: (_, r: RouteRule) => <span className="mono">{ruleTarget(r)}</span> },
          { title: '状态', dataIndex: 'enabled', width: 90, render: (v: boolean) => <Status tone={v ? 'ok' : 'off'}>{v ? '启用' : '停用'}</Status> },
          { title: '', width: 88, align: 'right', render: (_, r: RouteRule) => rowActions(() => { setRuleEditing(r); setRuleOpen(true) }, () => onDeleteRule(r), `规则 ${r.id}`) },
        ]}
      />
      <div className="sd-final">
        <span>其余流量</span>
        <Select
          size="small"
          aria-label="兜底出站"
          value={finalOutbound}
          disabled={rawMode || busy === 'final'}
          loading={busy === 'final'}
          popupMatchSelectWidth={false}
          onChange={(v) => { if (v !== finalOutbound) void onSetFinal(v) }}
          options={[...new Set(['direct', finalOutbound, ...outboundTags])].map((tag) => ({ value: tag, label: tag }))}
        />
      </div>
    </>
  )

  const views: Record<DetailSection, ReactNode> = {
    inbounds: inboundTable,
    outbounds: outboundTable,
    rules: rulesTable,
    traffic: <ServerTraffic key={`traffic-${sid}`} serverId={sid} />,
    logs: <Logs key={`logs-${sid}`} serverId={sid} embedded />,
  }

  return (
    <RequestState loading={pageLoading} error={loadError || sectionError} hasData onRetry={loadCurrentServer}>
    <div className="sd-page">
      <div className="sd-toolbar">
        <Pills
          label="服务器"
          active={section}
          onChange={goSection}
          actions={undefined}
          items={[
            { key: 'inbounds', label: '入站', count: server.inbounds?.length || 0 },
            { key: 'outbounds', label: '出站', count: outbounds.length },
            { key: 'rules', label: '路由', count: rules.length },
            { key: 'traffic', label: '流量' },
            { key: 'logs', label: '日志' },
          ]}
        />
        <div className="sd-toolbar-actions">
          {addButton[section]}
          <ServerOverviewCard
            server={server}
            busy={busy}
            refreshing={refreshing}
            onRefresh={refresh}
            onInstallSingbox={() => setInstallOpen(true)}
            onUninstallSingbox={confirmUninstallSingbox}
            onInstallAgent={installOrUpgradeAgent}
            onUninstallAgent={confirmUninstallAgent}
            onImport={openImport}
            onEditConfig={openConfigEditor}
            onRestart={() => run('restart', () => serviceAction(sid, 'restart'), 'sing-box 已重启')}
            onExport={() => setFmtOpen(true)}
          />
        </div>
      </div>

      <Stats items={[
        { label: '下行速率', value: server.online && server.traffic_available ? `${formatBytes(server.traffic_download_rate ?? 0)}/s` : '—' },
        { label: '上行速率', value: server.online && server.traffic_available ? `${formatBytes(server.traffic_upload_rate ?? 0)}/s` : '—' },
        { label: '负载', value: server.online ? server.load1?.toFixed(2) : '—' },
        { label: '内存', value: server.online && memPct !== null ? `${memPct}%` : '—', hint: server.online && server.mem_total ? `${formatBytes(server.mem_used)} / ${formatBytes(server.mem_total)}` : undefined },
      ]} />

      {rawMode && (
        <Alert
          type="warning"
          showIcon
          className="sd-raw"
          message="原始配置模式，面板编辑已锁定"
          action={<Button size="small" danger loading={busy === 'mode'} onClick={switchToManaged}>切换到面板管理</Button>}
        />
      )}
      {views[section]}

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
        key={cfgSessionID}
        open={cfgOpen}
        loading={cfgLoading}
        saving={cfgSaving}
        text={cfgText}
        onText={setCfgText}
        onCancel={closeConfigEditor}
        onSave={saveConfig}
      />

      <InstallSingboxModal
        open={installOpen}
        form={installForm}
        onCancel={() => setInstallOpen(false)}
        onConfirm={doInstall}
      />

      <NodeFormatsExportModal serverId={sid} open={fmtOpen} onCancel={() => setFmtOpen(false)} />

      <span id="rule-sort-help" className="sr-only">使用上方向键或下方向键调整规则顺序</span>
      <div className="sr-only" aria-live="polite" aria-atomic="true">{sortAnnouncement}</div>
    </div>
    </RequestState>
  )
}
