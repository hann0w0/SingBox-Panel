import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
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
  Switch,
  Table,
  Typography,
  message,
} from '../../../ui'
import {
  Copy,
  Eye,
  Trash2,
  Pencil,
  GripVertical,
  Plus,
} from 'lucide-react'
import { useNavigate, useParams } from 'react-router-dom'
import {
  applyRawConfig,
  deleteInbound,
  deleteRule,
  errMsg,
  getServer,
  importConfig,
  installSingbox,
  isCanceledRequest,
  listRuleSets,
  listRoutingNodes,
  listRules,
  remoteConfig,
  reorderRules,
  serverStatus,
  serviceAction,
  setConfigMode,
  setFinalOutbound,
  uninstallAgent,
  uninstallSingbox,
  updateAgent,
  updateRule,
  updateServer,
} from '../../../api'
import type { ImportSummary } from '../../../api'
import { applyWithToast, isConfigApplyResult, showConfigApplyResult } from '../../../configApply'
import type { Inbound, RoutingNode, RouteRule, RuleSet, Server, TrafficSeries } from '../../../types'
import InboundForm from './InboundForm'
import { RuleForm } from './RoutingForms'
import { ConfigEditorModal, ImportPreviewModal, InstallSingboxModal, ServerEditModal } from './ServerDialogs'
import { NodeFormatsExportModal } from './NodeFormatsModal'
import { ServerOverviewCard } from './ServerOverviewCard'
import ServerTraffic from './ServerTraffic'
import Logs from '../logs/Logs'
import { copyToClipboard, formatBytes } from '../../../util'
import { Pills } from '../../../ui/Page'
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

const DETAIL_SECTIONS = ['inbounds', 'rules', 'logs'] as const
type DetailSection = typeof DETAIL_SECTIONS[number]

function ServerDetailSkeleton() {
  return (
    <div className="sd-page sd-skeleton" role="status" aria-label="加载中">
      <div className="ui-page-head">
        <div className="ui-page-title">
          <span className="sd-skel-bar sd-skel-title" />
          <span className="sd-skel-bar sd-skel-meta" />
        </div>
        <div className="ui-page-actions">
          <span className="sd-skel-bar sd-skel-btn-icon" />
          <span className="sd-skel-bar sd-skel-btn" />
          <span className="sd-skel-bar sd-skel-btn" />
        </div>
      </div>

      <div className="tr-overview">
        <div className="ui-stats">
          {Array.from({ length: 4 }).map((_, i) => (
            <div key={i} className="ui-stat">
              <span className="sd-skel-bar sd-skel-stat-label" />
              <span className="sd-skel-bar sd-skel-stat-value" />
              <span className="sd-skel-bar sd-skel-stat-hint" />
            </div>
          ))}
        </div>
        <div className="tr-grid">
          {Array.from({ length: 2 }).map((_, i) => (
            <section key={i} className="ui-card tr-panel">
              <header className="tr-panel-head">
                <span className="sd-skel-bar sd-skel-panel-title" />
                <span className="sd-skel-bar sd-skel-panel-ctrl" />
              </header>
              <span className="sd-skel-bar sd-skel-chart" />
            </section>
          ))}
        </div>
      </div>

      <div className="ui-pills-row">
        <span className="sd-skel-bar sd-skel-pills" />
        <span className="sd-skel-bar sd-skel-add" />
      </div>

      <div className="ui-table-wrapper">
        <div className="sd-skel-table-head">
          <span className="sd-skel-bar sd-skel-cell" />
          <span className="sd-skel-bar sd-skel-cell-sm" />
          <span className="sd-skel-bar sd-skel-cell-sm" />
          <span className="sd-skel-bar sd-skel-cell-right" />
        </div>
        {Array.from({ length: 3 }).map((_, i) => (
          <div key={i} className="sd-skel-table-row">
            <span className="sd-skel-bar sd-skel-cell" />
            <span className="sd-skel-bar sd-skel-cell-sm" />
            <span className="sd-skel-bar sd-skel-cell-sm" />
            <span className="sd-skel-bar sd-skel-cell-right" />
          </div>
        ))}
      </div>
    </div>
  )
}

export default function ServerDetail() {
  const { id, section: sectionParam } = useParams()
  const navigate = useNavigate()
  const section: DetailSection = (DETAIL_SECTIONS as readonly string[]).includes(sectionParam || '') ? sectionParam as DetailSection : 'inbounds'
  const sid = Number(id)
  useEffect(() => {
    if (sectionParam === 'outbounds') navigate(`/admin/servers/${id}/rules`, { replace: true })
    if (sectionParam === 'traffic') navigate(`/admin/servers/${id}`, { replace: true })
    if (sectionParam === 'rulesets') navigate('/admin/rulesets', { replace: true })
  }, [id, sectionParam, navigate])
  const [server, setServer] = useState<Server | null>(null)
  const [trafficData, setTrafficData] = useState<TrafficSeries | null>(null)
  const [trafficReloadKey, setTrafficReloadKey] = useState(0)
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
  const [cfgSessionID, setCfgSessionID] = useState(0)
  const cfgSessionRef = useRef<{
    serverID: number
    request: AbortController | null
    saving: boolean
  } | null>(null)

  // 节点详情与编辑弹窗状态
  const [viewingInbound, setViewingInbound] = useState<Inbound | null>(null)
  const [serverEditOpen, setServerEditOpen] = useState(false)
  const [serverEditSaving, setServerEditSaving] = useState(false)
  const [serverEditForm] = Form.useForm()
  const serverEditLockRef = useRef(false)

  const [routingNodes, setRoutingNodes] = useState<RoutingNode[]>([])
  const [rules, setRules] = useState<RouteRule[]>([])
  const [ruleSets, setRuleSets] = useState<RuleSet[]>([])
  const [ruleOpen, setRuleOpen] = useState(false)
  const [ruleEditing, setRuleEditing] = useState<RouteRule | null>(null)
  const [togglingRuleIDs, setTogglingRuleIDs] = useState<Set<number>>(() => new Set())
  const [importOpen, setImportOpen] = useState(false)
  const [importSum, setImportSum] = useState<ImportSummary | null>(null)
  const [importing, setImporting] = useState(false)

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
      setPublicURL(d.public_url)
      const [nodeRows, ruleRows, ruleSetRows] = await Promise.allSettled([
        listRoutingNodes(controller.signal, targetSID),
        listRules(targetSID, controller.signal),
        listRuleSets(controller.signal),
      ])
      if (generation !== loadGenerationRef.current || currentSIDRef.current !== targetSID) return
      if (nodeRows.status === 'fulfilled') setRoutingNodes(nodeRows.value)
      if (ruleRows.status === 'fulfilled' && ruleOrderGeneration === ruleOrderGenerationRef.current) {
        ruleOrderRef.current = ruleRows.value
        setRules(ruleRows.value)
      }
      if (ruleSetRows.status === 'fulfilled') setRuleSets(ruleSetRows.value)
      const failures = [
        nodeRows.status === 'rejected' ? `节点：${errMsg(nodeRows.reason)}` : '',
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
    setTrafficData(null)
    setRoutingNodes([])
    setRules([])
    ruleOrderRef.current = []
    setRuleSets([])
    setRefreshing(false)
    setCfgOpen(false)
    setCfgText('')
    setCfgLoading(false)
    setCfgSaving(false)
    setViewingInbound(null)
    setServerEditOpen(false)
    setServerEditSaving(false)
    setImportOpen(false)
    setImportSum(null)
    setImporting(false)
    setFormOpen(false)
    setEditing(null)
    setRuleOpen(false)
    setRuleEditing(null)
    setTogglingRuleIDs(new Set())
    setBusy('')
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
      setTrafficReloadKey((value) => value + 1)
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

  const onToggleRule = async (r: RouteRule, enabled: boolean) => {
    const targetSID = sid
    if (currentSIDRef.current !== targetSID || togglingRuleIDs.has(r.id)) return
    setTogglingRuleIDs((prev) => new Set(prev).add(r.id))
    const patchRules = (list: RouteRule[], nextEnabled: boolean) =>
      list.map((item) => (item.id === r.id ? { ...item, enabled: nextEnabled } : item))
    ruleOrderRef.current = patchRules(ruleOrderRef.current, enabled)
    setRules((prev) => patchRules(prev, enabled))
    try {
      const saved = await applyWithToast(`rule-toggle-${r.id}`, () =>
        updateRule(targetSID, r.id, {
          match: r.match,
          outbound: r.outbound,
          remark: r.remark || '',
          sort: r.sort,
          enabled,
        }),
      )
      if (currentSIDRef.current !== targetSID) return
      if (!saved) {
        ruleOrderRef.current = patchRules(ruleOrderRef.current, r.enabled)
        setRules((prev) => patchRules(prev, r.enabled))
      } else {
        loadCurrentServer()
      }
    } finally {
      if (currentSIDRef.current === targetSID) {
        setTogglingRuleIDs((prev) => {
          const next = new Set(prev)
          next.delete(r.id)
          return next
        })
      }
    }
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

  const openEditServer = () => {
    if (!server) return
    serverEditForm.resetFields()
    serverEditForm.setFieldsValue({
      name: server.name,
      address: server.address,
      remark: server.remark,
      traffic_reset_day: server.traffic_reset_day ?? 1,
    })
    setServerEditOpen(true)
  }

  const submitEditServer = async () => {
    if (serverEditLockRef.current) return
    const targetSID = sid
    serverEditLockRef.current = true
    setServerEditSaving(true)
    try {
      const values = await serverEditForm.validateFields()
      await updateServer(targetSID, values)
      if (currentSIDRef.current !== targetSID) return
      message.success('已保存')
      setServerEditOpen(false)
      setTrafficReloadKey((value) => value + 1)
      loadCurrentServer()
    } catch (e) {
      if (currentSIDRef.current === targetSID) message.error(errMsg(e))
    } finally {
      serverEditLockRef.current = false
      if (currentSIDRef.current === targetSID) setServerEditSaving(false)
    }
  }



  const portTrafficById = useMemo(() => {
    const map = new Map<number, { upload: number; download: number }>()
    for (const port of trafficData?.ports ?? []) {
      map.set(port.inbound_id, { upload: port.upload, download: port.download })
    }
    return map
  }, [trafficData])

  if (!server) {
    if (pageLoading) return <ServerDetailSkeleton />
    return <RequestState loading={false} error={loadError} hasData={false} onRetry={loadCurrentServer}><span /></RequestState>
  }
  const rawMode = server.config_mode === 'raw'
  const finalOutbound = server.final_outbound || 'direct'
  const trafficAvailable = !!trafficData && (!!trafficData.available || !!trafficData.updated_at)
  const unattributedUpload = trafficData?.unattributed_upload ?? 0
  const unattributedDownload = trafficData?.unattributed_download ?? 0
  const goSection = (key: string) => navigate(key === 'inbounds' ? `/admin/servers/${sid}` : `/admin/servers/${sid}/${key}`)
  const addButton: Record<DetailSection, ReactNode> = {
    inbounds: <Button type="primary" icon={<Plus />} disabled={rawMode} onClick={() => { setEditing(null); setFormOpen(true) }}>新增入站</Button>,
    rules: <Button type="primary" icon={<Plus />} disabled={rawMode} onClick={() => { setRuleEditing(null); setRuleOpen(true) }}>新增规则</Button>,
    logs: null,
  }
  const rowActions = (onEdit: () => void, onDelete: () => void, label: string, onView?: () => void) => (
    <span className="ui-row-actions">
      {onView && <Button size="small" type="text" icon={<Eye />} title="查看详情" aria-label={`查看 ${label}`} onClick={onView} />}
      <Button size="small" type="text" icon={<Pencil />} aria-label={`编辑 ${label}`} disabled={rawMode} onClick={onEdit} />
      <Button size="small" type="text" icon={<Trash2 />} aria-label={`删除 ${label}`} disabled={rawMode} onClick={onDelete} />
    </span>
  )

  const inboundTable = (
    <Table
      className="tr-port-table"
      rowKey="id"
      scroll={{ x: 760 }}
      dataSource={server.inbounds || []}
      locale={{ emptyText: '暂无入站' }}
      rowClassName={(ib: Inbound) => (ib.enabled === false ? 'is-disabled' : '')}
      columns={[
        { title: '标签', dataIndex: 'tag', render: (v: string) => <span className="sd-strong">{v}</span> },
        { title: '协议', dataIndex: 'type', render: (v: string) => <ProtocolTag type={v} /> },
        { title: '端口', dataIndex: 'listen_port', sorter: (a: Inbound, b: Inbound) => a.listen_port - b.listen_port, render: (v: number) => <span className="mono">{v}</span> },
        { title: '用户', render: (_, ib: Inbound) => <span className="muted">{ib.settings?.multi_user ? '多用户' : '单用户'}</span> },
        {
          title: '下载',
          align: 'right',
          sorter: (a: Inbound, b: Inbound) => (portTrafficById.get(a.id)?.download ?? 0) - (portTrafficById.get(b.id)?.download ?? 0),
          render: (_, ib: Inbound) => <span className="mono">{trafficAvailable ? formatBytes(portTrafficById.get(ib.id)?.download ?? 0) : '—'}</span>,
        },
        {
          title: '上传',
          align: 'right',
          sorter: (a: Inbound, b: Inbound) => (portTrafficById.get(a.id)?.upload ?? 0) - (portTrafficById.get(b.id)?.upload ?? 0),
          render: (_, ib: Inbound) => <span className="mono muted">{trafficAvailable ? formatBytes(portTrafficById.get(ib.id)?.upload ?? 0) : '—'}</span>,
        },
        {
          title: '合计',
          align: 'right',
          sorter: (a: Inbound, b: Inbound) => {
            const totalA = (portTrafficById.get(a.id)?.upload ?? 0) + (portTrafficById.get(a.id)?.download ?? 0)
            const totalB = (portTrafficById.get(b.id)?.upload ?? 0) + (portTrafficById.get(b.id)?.download ?? 0)
            return totalA - totalB
          },
          render: (_, ib: Inbound) => {
            const traffic = portTrafficById.get(ib.id)
            return <span className="mono sd-strong">{trafficAvailable ? formatBytes((traffic?.upload ?? 0) + (traffic?.download ?? 0)) : '—'}</span>
          },
        },
        {
          title: '',
          width: 112,
          align: 'right',
          render: (_, ib: Inbound) => rowActions(
            () => { setEditing(ib); setFormOpen(true) },
            () => onDeleteInbound(ib),
            `入站 ${ib.tag}`,
            () => setViewingInbound(ib),
          ),
        },
      ]}
      summary={() => (trafficAvailable && unattributedUpload + unattributedDownload > 0 ? (
        <Table.Summary fixed>
          <Table.Summary.Row className="tr-summary-row">
            <Table.Summary.Cell index={0}><span className="muted">未归属</span></Table.Summary.Cell>
            <Table.Summary.Cell index={1}><span className="faint">—</span></Table.Summary.Cell>
            <Table.Summary.Cell index={2}><span className="faint">—</span></Table.Summary.Cell>
            <Table.Summary.Cell index={3}><span className="faint">—</span></Table.Summary.Cell>
            <Table.Summary.Cell index={4} align="right"><span className="mono">{formatBytes(unattributedDownload)}</span></Table.Summary.Cell>
            <Table.Summary.Cell index={5} align="right"><span className="mono muted">{formatBytes(unattributedUpload)}</span></Table.Summary.Cell>
            <Table.Summary.Cell index={6} align="right"><span className="mono sd-strong">{formatBytes(unattributedUpload + unattributedDownload)}</span></Table.Summary.Cell>
            <Table.Summary.Cell index={7} />
          </Table.Summary.Row>
        </Table.Summary>
      ) : null)}
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
    if (v === 'direct') return 'Direct · 直出'
    return routingNodes.find((node) => node.tag === v)?.name || v
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
          { title: '备注', dataIndex: 'remark', width: 180, render: (v: string) => v ? <span className="muted">{v}</span> : <span className="faint">—</span> },
          {
            title: '状态',
            dataIndex: 'enabled',
            width: 90,
            render: (v: boolean, r: RouteRule) => (
              <Switch
                size="small"
                checked={v}
                disabled={rawMode || togglingRuleIDs.has(r.id)}
                aria-label={`${v ? '停用' : '启用'}规则 ${r.remark || r.id}`}
                onChange={(checked) => void onToggleRule(r, checked)}
              />
            ),
          },
          { title: '', width: 88, align: 'right', render: (_, r: RouteRule) => rowActions(() => { setRuleEditing(r); setRuleOpen(true) }, () => onDeleteRule(r), `规则 ${r.id}`) },
        ]}
      />
      <div className="sd-final">
        <span>默认出站流量</span>
        <Select
          size="small"
          aria-label="默认出站流量"
          showSearch
          value={finalOutbound}
          disabled={rawMode || busy === 'final'}
          loading={busy === 'final'}
          popupMatchSelectWidth={false}
          onChange={(v) => { if (v !== finalOutbound) void onSetFinal(v) }}
          options={[...new Set(['direct', finalOutbound, ...routingNodes.map((node) => node.tag)])].map((tag) => ({
            value: tag,
            label: tag === 'direct' ? 'Direct · 直出' : (routingNodes.find((node) => node.tag === tag)?.name || tag),
          }))}
        />
      </div>
    </>
  )

  const views: Record<DetailSection, ReactNode> = {
    inbounds: inboundTable,
    rules: rulesTable,
    logs: <Logs key={sid} serverId={sid} serverOnline={server.online} />,
  }

  return (
    <RequestState loading={pageLoading} error={loadError || sectionError} hasData onRetry={loadCurrentServer}>
    <div className="sd-page">
      <ServerOverviewCard
        server={server}
        publicURL={publicURL}
        busy={busy}
        refreshing={refreshing}
        onRefresh={refresh}
        onEditServer={openEditServer}
        onInstallSingbox={() => setInstallOpen(true)}
        onUninstallSingbox={confirmUninstallSingbox}
        onInstallAgent={installOrUpgradeAgent}
        onUninstallAgent={confirmUninstallAgent}
        onImport={openImport}
        onEditConfig={openConfigEditor}
        onRestart={() => run('restart', () => serviceAction(sid, 'restart'), 'sing-box 已重启')}
      />

      <ServerTraffic
        key={sid}
        serverId={sid}
        server={server}
        reloadKey={trafficReloadKey}
        onDataChange={setTrafficData}
      />

      {rawMode && (
        <Alert
          type="warning"
          showIcon
          className="sd-raw"
          message="原始配置模式，面板编辑已锁定"
          action={<Button size="small" danger loading={busy === 'mode'} onClick={switchToManaged}>切换到面板管理</Button>}
        />
      )}

      <Pills
        label="服务器配置"
        active={section}
        onChange={goSection}
        actions={addButton[section]}
        items={[
          { key: 'inbounds', label: '入站', count: server.inbounds?.length || 0 },
          { key: 'rules', label: '路由', count: rules.length },
          { key: 'logs', label: '日志' },
        ]}
      />
      {views[section]}

      <InboundForm
        key={`inbound-${sid}`}
        serverId={sid}
        inbound={editing}
        open={formOpen}
        onClose={() => setFormOpen(false)}
        onSaved={loadCurrentServer}
      />

      <RuleForm
        key={`rule-${sid}`}
        serverId={sid}
        rule={ruleEditing}
        routingNodes={routingNodes}
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

      <ServerEditModal
        title={`编辑 ${server.name}`}
        open={serverEditOpen}
        saving={serverEditSaving}
        form={serverEditForm}
        onCancel={() => setServerEditOpen(false)}
        onConfirm={submitEditServer}
      />

      <NodeFormatsExportModal
        serverId={sid}
        open={!!viewingInbound}
        inboundTag={viewingInbound?.tag}
        multiUser={Boolean(viewingInbound?.settings?.multi_user)}
        onCancel={() => setViewingInbound(null)}
      />

      <span id="rule-sort-help" className="sr-only">使用上方向键或下方向键调整规则顺序</span>
      <div className="sr-only" aria-live="polite" aria-atomic="true">{sortAnnouncement}</div>
    </div>
    </RequestState>
  )
}
