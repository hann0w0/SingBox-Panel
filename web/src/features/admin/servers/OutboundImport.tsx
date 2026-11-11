import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { Textarea as TextareaPrimitive } from '@/components/ui/textarea'
import { Button as ShadcnButton } from '@/components/ui/button'
import { Alert, Button, Form, Input, Space } from '../../../ui'
import { createOutbound, errMsg, listOutboundSources, previewOutboundImport } from '../../../api'
import type { OutboundBody, OutboundSource, OutboundSourceKind } from '../../../api'
import { showConfigApplyResult } from '../../../configApply'

export type NodeImportMode = 'panel' | 'external' | 'uri'

/**
 * 新增出站 -> 节点导入的三种来源，每个来源都是对话框顶部的独立标签：
 *   - 面板节点 / 外部节点: 保存后的出站跟随该节点，源节点改动即时生效；
 *   - URI: 解析后的独立快照。
 */
export function OutboundImport({ serverId, mode, onClose, onSaved, onSavingChange, formId, onReadyChange }: {
  serverId: number
  mode: NodeImportMode
  onClose: () => void
  onSaved: () => void
  onSavingChange?: (saving: boolean) => void
  formId?: string
  onReadyChange?: (ready: boolean) => void
}) {
  const [sources, setSources] = useState<OutboundSource[]>([])
  const [sourcesLoading, setSourcesLoading] = useState(false)
  const [sourcesError, setSourcesError] = useState('')
  const [sourceId, setSourceId] = useState<number | undefined>(undefined)
  // Groups stay collapsed (as on the node access editor) so a long list of
  // servers does not push the dialog open.
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(() => new Set())
  const [uri, setURI] = useState('')
  const [preview, setPreview] = useState<OutboundBody | null>(null)
  const [tag, setTag] = useState('')
  const [error, setError] = useState('')
  const [parsing, setParsing] = useState(false)
  const [saving, setSaving] = useState(false)
  // Remember which source produced the preview so the save keeps that link.
  // The preview response carries the node's snapshot, not the reference.
  const [previewSource, setPreviewSource] = useState<{ kind: OutboundSourceKind; id: number } | null>(null)
  const request = useRef<AbortController | null>(null)
  const saveLock = useRef(false)
  const session = useRef(0)

  useLayoutEffect(() => {
    session.current += 1
    return () => {
      session.current += 1
      request.current?.abort()
    }
  }, [serverId])

  const loadSources = async (signal: AbortSignal) => {
    setSourcesLoading(true)
    setSourcesError('')
    try {
      const rows = await listOutboundSources(serverId, signal)
      if (!signal.aborted) setSources(rows)
    } catch (e) {
      if (!signal.aborted) setSourcesError(errMsg(e))
    } finally {
      if (!signal.aborted) setSourcesLoading(false)
    }
  }

  useEffect(() => {
    const controller = new AbortController()
    void loadSources(controller.signal)
    return () => controller.abort()
  }, [serverId])

  const invalidate = () => {
    request.current?.abort()
    setParsing(false)
    setPreview(null)
    setPreviewSource(null)
    setTag('')
    setError('')
  }

  const parse = async (body: Parameters<typeof previewOutboundImport>[1]) => {
    invalidate()
    const controller = new AbortController()
    request.current = controller
    setParsing(true)
    try {
      const result = await previewOutboundImport(serverId, body, controller.signal)
      if (controller.signal.aborted) return
      setPreview(result)
      setTag(result.tag)
      setPreviewSource('source_kind' in body ? { kind: body.source_kind, id: body.source_id } : null)
    } catch (e) {
      if (!controller.signal.aborted) setError(errMsg(e))
    } finally {
      if (!controller.signal.aborted) setParsing(false)
    }
  }

  const chooseSource = (source: OutboundSource) => {
    if (source.disabled) return
    setSourceId(source.id)
    void parse({ source_kind: source.kind, source_id: source.id })
  }

  const save = async () => {
    if (!preview || !tag.trim() || saveLock.current) return
    const savingSession = session.current
    saveLock.current = true
    setSaving(true)
    onSavingChange?.(true)
    setError('')
    try {
      // Preserve the entire parsed payload, including parameters absent from
      // the compact manual form. A failed save leaves the draft available.
      // Imported nodes also record their source so the outbound keeps following
      // it; a URI import stores an independent snapshot.
      const result = await createOutbound(serverId, {
        ...preview,
        tag: tag.trim(),
        ...(previewSource ? { source_kind: previewSource.kind, source_id: previewSource.id } : {}),
      })
      if (savingSession !== session.current) return
      showConfigApplyResult(result)
      onSaved()
      onClose()
    } catch (e) {
      if (savingSession === session.current) setError(errMsg(e))
    } finally {
      if (savingSession === session.current) {
        saveLock.current = false
        setSaving(false)
        onSavingChange?.(false)
      }
    }
  }

  const canSave = !!preview && !!tag.trim() && !parsing
  useEffect(() => { onReadyChange?.(canSave) }, [canSave, onReadyChange])

  const panelSources = useMemo(() => sources.filter((s) => s.kind === 'inbound'), [sources])
  const externalSources = useMemo(() => sources.filter((s) => s.kind === 'custom_node'), [sources])
  const modeSources = mode === 'panel' ? panelSources : externalSources
  const groups = useMemo(() => {
    const out: { label: string; options: OutboundSource[] }[] = []
    for (const source of modeSources) {
      const label = (source.group || '').trim() || '未分组'
      const group = out.find((g) => g.label === label)
      if (group) group.options.push(source)
      else out.push({ label, options: [source] })
    }
    return out
  }, [modeSources])
  const settings = preview?.settings
  return (
    <Form id={formId} layout="vertical" className="fm-form fm-import-form" onFinish={save}>
      {mode === 'uri' ? (
        <div className="flex flex-col">
          <div className="flex h-[264px] flex-col gap-2 rounded-lg border bg-background p-2">
            <TextareaPrimitive
              aria-label="节点 URI"
              placeholder="粘贴节点链接，例如 vless://…"
              className="mono min-h-0 flex-1 resize-none border-0 shadow-none focus-visible:ring-0"
              value={uri}
              disabled={saving}
              onChange={(e) => { invalidate(); setURI(e.target.value) }}
            />
            <Button type="default" size="small" className="self-start" loading={parsing} disabled={!uri.trim() || saving} onClick={() => void parse({ uri: uri.trim() })}>解析链接</Button>
          </div>
        </div>
      ) : (
        <div className="flex flex-col">
          <div
            className="h-[264px] overflow-y-auto overscroll-contain rounded-lg border bg-background"
            aria-label={mode === 'panel' ? '面板节点' : '外部节点'}
          >
            {sourcesLoading ? (
              <div className="flex h-full items-center justify-center text-sm text-muted-foreground">正在加载节点…</div>
            ) : groups.length === 0 ? (
              <div className="flex h-full items-center justify-center text-sm text-muted-foreground" role="status">
                {mode === 'panel' ? '暂无其它服务器的节点' : '暂无外部节点'}
              </div>
            ) : groups.map((group) => {
              const open = expandedGroups.has(group.label)
              return (
                <div key={group.label} className="border-b last:border-b-0">
                  <button
                    type="button"
                    className="flex min-h-11 w-full items-center gap-2 px-3 text-left outline-none transition-colors hover:bg-muted/60 focus-visible:ring-[3px] focus-visible:ring-ring/50"
                    aria-expanded={open}
                    aria-label={`${open ? '收起' : '展开'} ${group.label}`}
                    onClick={() => setExpandedGroups((current) => {
                      const next = new Set(current)
                      if (next.has(group.label)) next.delete(group.label)
                      else next.add(group.label)
                      return next
                    })}
                  >
                    {open ? <ChevronDown className="size-4 shrink-0 text-muted-foreground" /> : <ChevronRight className="size-4 shrink-0 text-muted-foreground" />}
                    <span className="min-w-0 flex-1 truncate text-sm font-medium">{group.label}</span>
                    <span className="shrink-0 text-xs tabular-nums text-muted-foreground">{group.options.length}</span>
                  </button>
                  {open && (
                    <div className="grid grid-cols-1 gap-1.5 px-3 pb-3 pl-9 sm:grid-cols-[repeat(auto-fill,minmax(184px,1fr))]" role="listbox" aria-label={group.label}>
                      {group.options.map((source) => {
                        const active = !source.disabled && sourceId === source.id
                        return (
                          <ShadcnButton
                            key={`${source.kind}-${source.id}`}
                            type="button"
                            role="option"
                            variant={active ? 'secondary' : 'outline'}
                            size="sm"
                            aria-selected={active}
                            aria-disabled={source.disabled || undefined}
                            disabled={source.disabled || saving}
                            className={`h-8 min-w-0 justify-start gap-2 rounded-md border-border bg-background px-2.5 font-normal shadow-xs hover:bg-muted/60 ${active ? 'border-ring bg-secondary ring-1 ring-ring/30' : ''}`}
                            title={source.unavailable_reason || undefined}
                            onClick={() => chooseSource(source)}
                          >
                            <span className="min-w-0 flex-1 truncate text-left text-[13px]">{source.name || `节点 ${source.id}`}</span>
                            {source.type ? <span className="shrink-0 text-[11px] text-muted-foreground">{source.type}</span> : null}
                            {source.disabled ? <span className="shrink-0 text-[11px] text-muted-foreground">不可用</span> : null}
                          </ShadcnButton>
                        )
                      })}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </div>
      )}

      {sourcesError && <Alert type="error" showIcon message={sourcesError} style={{ marginBottom: 16 }} />}
      {error && <Alert type="error" showIcon message={error} style={{ marginBottom: 16 }} />}
      {preview && (
        <>
          <section className="fm-import-preview">
            <h3>
              连接详情 <span className="ui-tag">{preview.type}</span>
            </h3>
            <dl className="ui-kv">
              <div><dt>服务器</dt><dd className="mono">{settings?.server}</dd></div>
              <div><dt>端口</dt><dd className="mono">{settings?.server_port}</dd></div>
              <div><dt>安全</dt><dd>{settings?.settings?.tls?.reality?.enabled ? 'REALITY' : settings?.settings?.tls?.enabled ? 'TLS' : '未启用 TLS'}</dd></div>
              {mode !== 'uri' && <div><dt>来源</dt><dd>{mode === 'panel' ? '面板节点' : '外部节点'} · 跟随节点</dd></div>}
            </dl>
          </section>
          <Form.Item label="出站标签" required>
            <Input aria-label="出站标签" value={tag} disabled={saving} onChange={(e) => setTag(e.target.value)} />
          </Form.Item>
        </>
      )}
      {!formId && <Space style={{ display: 'flex', justifyContent: 'flex-end' }}>
        <Button onClick={onClose} disabled={saving}>取消</Button>
        <Button type="primary" htmlType="submit" loading={saving} disabled={!preview || !tag.trim() || parsing}>保存出站</Button>
      </Space>}
    </Form>
  )
}
