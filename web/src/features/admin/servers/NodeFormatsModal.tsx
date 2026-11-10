import { useEffect, useRef, useState } from 'react'
import { Button, Modal, Select, Tooltip, Typography, message } from '../../../ui'
import { Check, Copy, Eye } from 'lucide-react'
import type { NodeFormats } from '../../../api'
import { errMsg, getNodeFormats } from '../../../api'
import { RequestState } from '../../../components/RequestState'
import { copyToClipboard } from '../../../util'
import '../../../styles/forms.css'
import { MODAL_LG, MODAL_MD } from '../../../ui/modal'

export function NodeFormatsExportModal({
  serverId,
  open,
  onCancel,
  inboundTag,
  multiUser,
}: {
  serverId: number
  open: boolean
  onCancel: () => void
  inboundTag?: string
  multiUser?: boolean
}) {
  const isSingleInbound = Boolean(inboundTag)
  return (
    <Modal
      className="fm-modal"
      title={inboundTag || '导出节点'}
      open={open}
      onCancel={onCancel}
      footer={isSingleInbound ? <Button onClick={onCancel}>关闭</Button> : null}
      width={isSingleInbound ? MODAL_MD : MODAL_LG}
    >
      {open && (
        <NodeExportSession
          key={`${serverId}-${inboundTag ?? ''}`}
          serverId={serverId}
          inboundTag={inboundTag}
          multiUser={multiUser}
        />
      )}
    </Modal>
  )
}

function NodeExportSession({
  serverId,
  inboundTag,
  multiUser,
}: {
  serverId: number
  inboundTag?: string
  multiUser?: boolean
}) {
  const [userId, setUserId] = useState(0)
  const [data, setData] = useState<NodeFormats | null>(null)
  const [users, setUsers] = useState<NodeFormats['users']>([])
  const [multiCount, setMultiCount] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError(null)
    setData(null)
    getNodeFormats(serverId, controller.signal, userId || undefined)
      .then((next) => {
        if (controller.signal.aborted) return
        setData(next)
        setUsers(next.users)
        setMultiCount(next.multi_user_count)
      })
      .catch((cause) => { if (!controller.signal.aborted) setError(errMsg(cause)) })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [serverId, userId, attempt])

  // Never show the previous account's credentials while its replacement loads.
  const current = data?.user_id === userId ? data : null
  const showUserSelect = inboundTag ? Boolean(multiUser) : multiCount > 0
  return <>
    {showUserSelect && <div className="fm-export-user">
      <Select aria-label="多用户凭证" value={userId} onChange={setUserId} showSearch options={[
        { value: 0, label: '选择用户以导出多用户节点' },
        ...users.map((user) => ({ value: user.id, label: user.email })),
      ]} />
      <p className="muted">{users.length
        ? '单凭证节点始终显示；多用户节点使用所选用户的实际凭证，仅包含该用户获授权的入站。'
        : '暂无可用的授权用户，请先在用户页分配此服务器的多用户入站。'}</p>
    </div>}
    <RequestState loading={loading} error={error} hasData={!!current} onRetry={() => setAttempt((value) => value + 1)}>
      {current && (inboundTag ? (
        <SingleInboundDetailContent key={`${userId}-${inboundTag}`} data={current} inboundTag={inboundTag} multiUser={multiUser} />
      ) : (
        <NodeFormatsModalContent key={userId} data={current} />
      ))}
    </RequestState>
  </>
}

function useCopyFormat() {
  const [copiedKey, setCopiedKey] = useState<string | null>(null)
  const copiedTimerRef = useRef<number | null>(null)
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      if (copiedTimerRef.current !== null) window.clearTimeout(copiedTimerRef.current)
    }
  }, [])

  const copy = (text: string, key: string, label: string) => {
    if (!text) return
    copyToClipboard(text)
      .then(() => {
        if (!mountedRef.current) return
        setCopiedKey(key)
        message.success(`已复制 ${label}`)
        if (copiedTimerRef.current !== null) window.clearTimeout(copiedTimerRef.current)
        copiedTimerRef.current = window.setTimeout(() => {
          copiedTimerRef.current = null
          setCopiedKey(null)
        }, 1800)
      })
      .catch(() => message.error('复制失败，请手动选择内容'))
  }

  return { copiedKey, copy }
}

function NodeFormatDetailView({ item }: { item: NodeFormats['items'][number] }) {
  const { copiedKey, copy } = useCopyFormat()
  const displayTag = item.tag || item.name || ''
  const detailParams = Object.entries(item.params || {}).filter(([key]) => !['服务器', '端口', '协议'].includes(key))
  const buttons = [
    { key: 'detail-uri', label: 'URI', value: item.uri },
    { key: 'detail-clash', label: 'Clash', value: item.clash },
    { key: 'detail-surge', label: 'Surge', value: item.surge },
  ]
  return (
    <div className="fm-detail-body">
      <div className="fm-kv">
        <dl className="ui-kv">
          <div><dt>地址</dt><dd><Typography.Text copyable>{item.server || '—'}</Typography.Text></dd></div>
          <div><dt>端口</dt><dd><Typography.Text copyable>{String(item.port)}</Typography.Text></dd></div>
          <div><dt>协议</dt><dd><span className="ui-tag">{item.type}</span></dd></div>
          {detailParams.map(([key, value]) => (
            <div key={key}>
              <dt>{key}</dt>
              <dd><Typography.Text copyable={{ text: value }}>{value || '—'}</Typography.Text></dd>
            </div>
          ))}
        </dl>
      </div>
      <div className="fm-detail-actions">
        {buttons.map((button) => button.value ? (
          <Button
            key={button.key}
            size="small"
            className={copiedKey === button.key ? 'is-copied' : undefined}
            icon={copiedKey === button.key ? <Check /> : <Copy />}
            aria-label={`复制 ${displayTag} ${button.label}`}
            onClick={() => copy(button.value, button.key, `${displayTag} ${button.label}`)}
          >
            {button.label}
          </Button>
        ) : (
          <Tooltip key={button.key} title={`${button.label} 不支持该协议`}>
            <Button size="small" disabled icon={<Copy />}>{button.label}</Button>
          </Tooltip>
        ))}
      </div>
    </div>
  )
}

function SingleInboundDetailContent({
  data,
  inboundTag,
  multiUser,
}: {
  data: NodeFormats
  inboundTag: string
  multiUser?: boolean
}) {
  const item = (data.items || []).find((entry) => entry.tag === inboundTag)
  if (!item) {
    return (
      <div className="ui-empty">
        {(multiUser || data.multi_user_count > 0) && !data.user_id
          ? '选择已授权用户后可导出多用户节点'
          : '暂无可导出的节点'}
      </div>
    )
  }
  return <NodeFormatDetailView item={item} />
}

export function NodeFormatsModalContent({ data }: { data: NodeFormats }) {
  const { copiedKey, copy } = useCopyFormat()
  const [detailItem, setDetailItem] = useState<NodeFormats['items'][number] | null>(null)

  const items = data.items || []
  return (
    <>
      {!items.length ? (
        <div className="ui-empty">{data.multi_user_count > 0 && !data.user_id ? '选择已授权用户后可导出多用户节点' : '暂无可导出的节点'}</div>
      ) : (
        <ul className="fm-nodes">
          {items.map((item, index) => {
            const displayTag = item.tag || item.name || ''
            const buttons = [
              { key: `uri-${index}`, label: 'URI', value: item.uri },
              { key: `clash-${index}`, label: 'Clash', value: item.clash },
              { key: `surge-${index}`, label: 'Surge', value: item.surge },
            ]
            return (
              <li className="fm-node" key={`${displayTag}-${index}`}>
                <div className="fm-node-main">
                  <span className="fm-node-name">{displayTag}</span>
                  {item.type && <span className="ui-tag">{item.type}</span>}
                  {item.server ? <span className="fm-node-addr mono">{item.server}:{item.port}</span> : null}
                </div>
                <div className="fm-node-actions">
                  <Button size="small" type="text" icon={<Eye />} aria-label={`查看 ${displayTag} 详情`} onClick={() => setDetailItem(item)}>详情</Button>
                  {buttons.map((button) => button.value ? (
                    <Button
                      key={button.key}
                      size="small"
                      className={copiedKey === button.key ? 'is-copied' : undefined}
                      icon={copiedKey === button.key ? <Check /> : <Copy />}
                      aria-label={`复制 ${displayTag} ${button.label}`}
                      onClick={() => copy(button.value, button.key, `${displayTag} ${button.label}`)}
                    >
                      {button.label}
                    </Button>
                  ) : (
                    <Tooltip key={button.key} title={`${button.label} 不支持该协议`}>
                      <Button size="small" disabled icon={<Copy />}>{button.label}</Button>
                    </Tooltip>
                  ))}
                </div>
              </li>
            )
          })}
        </ul>
      )}

      <Modal
        className="fm-modal"
        title={detailItem?.tag || detailItem?.name || '节点详情'}
        open={!!detailItem}
        onCancel={() => setDetailItem(null)}
        footer={<Button onClick={() => setDetailItem(null)}>关闭</Button>}
        width={MODAL_MD}
      >
        {detailItem && <NodeFormatDetailView item={detailItem} />}
      </Modal>
    </>
  )
}
