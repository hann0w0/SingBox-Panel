import { useEffect, useMemo, useRef, useState } from 'react'
import { Collapsible } from 'radix-ui'
import { ChevronDown, ChevronsUpDown, Clock3, History, MapPin } from 'lucide-react'
import dayjs from 'dayjs'
import { Button, Input, Segmented } from '../../../components/kit'
import { DropdownMenu, DropdownMenuContent, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger } from '../../../components/ui/dropdown-menu'
import { RegionFlag } from '../../../components/RegionFlag'
import { RequestState } from '../../../components/RequestState'
import { errMsg, getUserSubFetchDetails, getUserSubFetches, getUserSubFetchLocation } from '../../../lib/api'
import type { SubscriptionFetch, SubscriptionHistorySummary, SubscriptionIPLocation, SubscriptionIPSummary } from '../../../lib/api'
import type { UserTrafficRange } from '../../../lib/types'
import { relativeTime } from '../../../lib/util'
import '../../../styles/subscription-history.css'

const ranges: { label: string; value: UserTrafficRange }[] = [
  { label: '24 小时', value: '24h' }, { label: '7 天', value: '7d' }, { label: '30 天', value: '30d' },
]
const sortOptions = [{ value: 'recent', label: '最近拉取' }, { value: 'count', label: '拉取次数' }] as const

export function subscriptionLocationLabel(geo?: SubscriptionIPLocation): string {
  if (!geo) return '正在查询…'
  if (geo.status === 'private') return '内网 / 保留地址'
  if (geo.status === 'error') return '位置查询失败'
  let country = geo.country
  if (geo.country_code && /^[A-Z]{2}$/.test(geo.country_code)) {
    country = new Intl.DisplayNames(['zh-CN'], { type: 'region' }).of(geo.country_code) || country
  }
  return [...new Set([country, geo.region, geo.city].filter(Boolean))].join(' · ') || '地区未知'
}

function FetchDetails({ userID, ip, range, until }: { userID: number; ip: string; range: UserTrafficRange; until: string }) {
  const [rows, setRows] = useState<SubscriptionFetch[]>([])
  const [cursor, setCursor] = useState('')
  const [loaded, setLoaded] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [reload, setReload] = useState(0)
  const request = useRef<AbortController | null>(null)
  const locked = useRef(false)
  const load = async (before?: string) => {
    if (locked.current) return
    locked.current = true
    const controller = new AbortController()
    request.current = controller
    setLoading(true); setError(null)
    try {
      const data = await getUserSubFetchDetails(userID, { ip, range, until, before, limit: before ? 10 : 6 }, controller.signal)
      if (controller.signal.aborted) return
      setRows(previous => before ? [...previous, ...data.fetches] : data.fetches)
      setCursor(data.next_cursor); setLoaded(true)
    } catch (cause) {
      if (!controller.signal.aborted) setError(errMsg(cause))
    } finally {
      if (request.current === controller) {
        locked.current = false
        if (!controller.signal.aborted) setLoading(false)
      }
    }
  }
  useEffect(() => {
    void load()
    return () => { request.current?.abort(); request.current = null; locked.current = false }
  }, [userID, ip, range, until, reload])
  return <RequestState loading={loading} error={error} hasData={loaded} empty={loaded && !rows.length} emptyDescription="暂无拉取记录" onRetry={() => loaded ? void load(cursor || undefined) : setReload(value => value + 1)}>
    <div className="sh-detail-table-wrap"><table className="sh-detail-table">
      <thead><tr><th>时间</th><th>客户端</th><th>格式</th><th>结果</th></tr></thead>
      <tbody>{rows.map(row => <tr key={row.id}><td><time>{dayjs(row.created_at).format('YYYY-MM-DD HH:mm:ss')}</time></td><td title={row.user_agent}>{row.client}</td><td className="muted">{row.format}</td><td>{row.status >= 200 && row.status < 300 ? '成功' : <span className="sh-fetch-error">失败 · {row.status}</span>}</td></tr>)}</tbody>
    </table></div>
    {cursor && <Button type="text" size="small" className="sh-more-records" loading={loading} onClick={() => void load(cursor)}>显示更多 <ChevronDown size={13} /></Button>}
  </RequestState>
}

function IPEntry({ group, geo, userID, range, until, onRetry }: { group: SubscriptionIPSummary; geo?: SubscriptionIPLocation; userID: number; range: UserTrafficRange; until: string; onRetry: () => void }) {
  const [open, setOpen] = useState(false)
  return <Collapsible.Root open={open} onOpenChange={setOpen} className="sh-ip-entry" data-ip={group.ip}>
    <Collapsible.Trigger className="sh-ip-row" aria-label={`${open ? '收起' : '展开'} ${group.ip || '未知 IP'} 的 ${group.fetches} 次拉取记录`}>
      <span className="sh-ip-address"><code>{group.ip || '未知 IP'}</code><small>{group.ip.includes(':') ? 'IPv6' : 'IPv4'}</small></span>
      <span className="sh-ip-location"><span>{geo?.country_code ? <RegionFlag code={geo.country_code} size={14} /> : <MapPin size={14} />}<span>{subscriptionLocationLabel(geo)}</span></span>{geo?.organization && <small title={geo.organization}>{geo.organization}</small>}</span>
      <span className="sh-ip-client"><span>{group.client}</span><small>{group.client_count > 1 ? `另有 ${group.client_count - 1} 种客户端` : group.format}</small></span>
      <span className="sh-ip-time" title={dayjs(group.last_seen).format('YYYY-MM-DD HH:mm:ss')}><span>{relativeTime(group.last_seen)}</span><small>{dayjs(group.last_seen).format('MM-DD HH:mm')}</small></span>
      <span className="sh-ip-count"><strong>{group.fetches.toLocaleString('zh-CN')}<small> 次</small></strong>{group.failures ? <small className="sh-fetch-error">{group.failures} 次失败</small> : <small>全部成功</small>}</span>
      <ChevronDown className="sh-row-chevron" size={16} />
    </Collapsible.Trigger>
    <Collapsible.Content className="sh-ip-details">
      <div className="sh-details-heading"><span><Clock3 size={13} /> 拉取明细 <span className="muted">· {group.fetches} 条</span></span></div>
      {open && <FetchDetails userID={userID} ip={group.ip} range={range} until={until} />}
      {geo?.status === 'error' && <Button type="text" size="small" className="sh-more-records" onClick={onRetry}>重试查询</Button>}
    </Collapsible.Content>
  </Collapsible.Root>
}

export function SubscriptionFetchHistory({ userID }: { userID: number }) {
  const [range, setRange] = useState<UserTrafficRange>('30d')
  const [data, setData] = useState<SubscriptionHistorySummary | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [reload, setReload] = useState(0)
  const [geos, setGeos] = useState<Record<string, SubscriptionIPLocation | undefined>>({})
  const [filter, setFilter] = useState('')
  const [sort, setSort] = useState<'recent' | 'count'>('recent')
  const [visible, setVisible] = useState(50)
  const retryRequests = useRef(new Map<string, AbortController>())
  useEffect(() => () => { retryRequests.current.forEach(controller => controller.abort()) }, [])
  useEffect(() => {
    const controller = new AbortController()
    setLoading(true); setError(null); setData(null); setVisible(50)
    getUserSubFetches(userID, range, controller.signal)
      .then(value => { if (!controller.signal.aborted) setData(value) })
      .catch(cause => { if (!controller.signal.aborted) setError(errMsg(cause)) })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [userID, range, reload])
  const groups = useMemo(() => (data?.ips || []).filter(group => `${group.ip} ${group.client} ${subscriptionLocationLabel(geos[group.ip])} ${geos[group.ip]?.organization || ''}`.toLowerCase().includes(filter.trim().toLowerCase()))
    .sort((a, b) => sort === 'count' ? b.fetches - a.fetches || Date.parse(b.last_seen) - Date.parse(a.last_seen) : Date.parse(b.last_seen) - Date.parse(a.last_seen)), [data, filter, sort, geos])
  // Include both the source page and displayed rows so sorting/search cannot
  // leave a visible IP outside the lookup queue. Never query all rows at once.
  const addresses = [...new Set([...(data?.ips.slice(0, visible) || []), ...groups.slice(0, visible)].map(group => group.ip))].sort().join(',')
  useEffect(() => {
    const controller = new AbortController()
    const queue = addresses.split(',').filter(ip => ip && !geos[ip])
    const work = async () => {
      while (queue.length && !controller.signal.aborted) {
        const ip = queue.shift()!
        let result: SubscriptionIPLocation
        try { result = await getUserSubFetchLocation(userID, ip, controller.signal) }
        catch { result = { ip, status: 'error' } }
        if (!controller.signal.aborted) setGeos(previous => ({ ...previous, [ip]: result }))
      }
    }
    void Promise.all(Array.from({ length: 3 }, work))
    return () => controller.abort()
  }, [userID, addresses])
  const retry = async (ip: string) => {
    if (retryRequests.current.has(ip)) return
    const controller = new AbortController()
    retryRequests.current.set(ip, controller)
    setGeos(previous => ({ ...previous, [ip]: undefined }))
    try {
      const result = await getUserSubFetchLocation(userID, ip, controller.signal)
      if (!controller.signal.aborted) setGeos(previous => ({ ...previous, [ip]: result }))
    } catch {
      if (!controller.signal.aborted) setGeos(previous => ({ ...previous, [ip]: { ip, status: 'error' } }))
    } finally { retryRequests.current.delete(ip) }
  }
  return <section className="um-group sh-history-section">
    <header className="um-group-head"><div className="sh-heading"><h4>订阅拉取</h4>{data && <span className="sh-section-meta" aria-live="polite">{data.total.toLocaleString('zh-CN')} 次拉取 · {data.ips.length} 个 IP</span>}</div></header>
    <div className="sh-history-toolbar"><Segmented options={ranges} value={range} onChange={setRange} /></div>
    <div className="sh-history-search">
      <Input aria-label="筛选 IP、地区或客户端" placeholder="筛选 IP、地区或客户端" value={filter} onChange={event => { setFilter(event.target.value); setVisible(50) }} />
      <div className="sh-sort-control"><span>排序</span><DropdownMenu><DropdownMenuTrigger asChild><Button type="text" size="small" className="sh-sort-trigger" aria-label={`排序：${sortOptions.find(option => option.value === sort)?.label}`}>{sortOptions.find(option => option.value === sort)?.label}<ChevronsUpDown size={14} /></Button></DropdownMenuTrigger>
        <DropdownMenuContent className="ui-dropdown ui-dropdown-menu sh-sort-menu" align="end" aria-label="排序方式"><DropdownMenuRadioGroup value={sort} onValueChange={value => { if (value === 'recent' || value === 'count') setSort(value) }}>{sortOptions.map(option => <DropdownMenuRadioItem key={option.value} value={option.value}>{option.label}</DropdownMenuRadioItem>)}</DropdownMenuRadioGroup></DropdownMenuContent>
      </DropdownMenu></div>
    </div>
    <RequestState loading={loading} error={error} hasData={!!data} onRetry={() => setReload(value => value + 1)}>
      {data && <div className="sh-ip-list">
        <div className="sh-ip-table-head" aria-hidden="true"><span>来源 IP</span><span>IP 位置 / 网络</span><span>最近客户端</span><span>最近拉取</span><span>拉取次数</span><span /></div>
        {groups.length ? groups.slice(0, visible).map(group => <IPEntry key={`${data.until}:${group.ip}`} group={group} geo={geos[group.ip] || (group.ip ? undefined : { ip: '', status: 'unknown' })} userID={userID} range={range} until={data.until} onRetry={() => void retry(group.ip)} />) : <div className="sh-empty-state"><History size={20} /><strong>{filter ? '没有匹配的拉取记录' : '暂无拉取记录'}</strong>{filter && <Button type="text" onClick={() => setFilter('')}>清除筛选</Button>}</div>}
      </div>}
      {data && visible < data.ips.length && <Button type="text" className="sh-more-records" onClick={() => setVisible(value => value + 50)}>加载更多 IP</Button>}
    </RequestState>
  </section>
}
