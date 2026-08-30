import axios from 'axios'
import type {
  Inbound,
  InboundSettings,
  InboundType,
  Outbound,
  OutboundSettings,
  OutboundType,
  Overview,
  RemoteConfig,
  RouteRule,
  RuleMatch,
  RuleSet,
  Server,
  StatusData,
  TrafficRange,
  TrafficSeries,
  User,
} from './types'
import { useAuth } from './store'

const http = axios.create({ baseURL: '' })

// 从 Zustand 内存读取 Token，避免每次请求同步阻塞主线程的 localStorage I/O
http.interceptors.request.use((config) => {
  const token = useAuth.getState().token
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

http.interceptors.response.use(
  (r) => r,
  (err) => {
    if (err?.response?.status === 401 && !location.pathname.startsWith('/login')) {
      useAuth.getState().logout()
      location.href = '/login'
    }
    return Promise.reject(err)
  },
)

// 从 axios 错误中提取人类可读的错误信息，使用类型安全的 isAxiosError 守卫
export function errMsg(e: unknown): string {
  if (axios.isAxiosError(e)) {
    return e.response?.data?.error || e.response?.data?.output || e.message || '请求失败'
  }
  return e instanceof Error ? e.message : '请求失败'
}

export function isCanceledRequest(e: unknown): boolean {
  return axios.isCancel(e) || (e instanceof DOMException && e.name === 'AbortError')
}

export interface ConfigApplyResult {
  apply_state: 'applied' | 'pending' | 'failed'
  apply_error?: string
}

// ---- auth ----
export const login = (username: string, password: string) =>
  http.post<{ token: string; user: User }>('/api/auth/login', { username, password }).then((r) => r.data)

// ---- current user ----
export const getMe = (signal?: AbortSignal) =>
  http
    .get<{ user: User; subscription_url: string }>('/api/user/me', { signal })
    .then((r) => r.data)

export interface UserNode {
  name: string
  type: string
  server: string
  port: number
  region?: string // ISO-ish code (US/HK/JP…) for dashboard map
  link: string
  params: Record<string, string>
}
export const getUserNodes = (signal?: AbortSignal) =>
  http.get<{ nodes: UserNode[] }>('/api/user/nodes', { signal }).then((r) => r.data.nodes)

export const resetSub = () =>
  http.post<{ subscription_url: string }>('/api/user/reset-sub').then((r) => r.data)

export const changePassword = (old_password: string, new_password: string) =>
  http.post<{ ok: boolean; token: string }>('/api/user/change-password', { old_password, new_password }).then((r) => r.data)

// ---- admin: overview ----
export const getOverview = (signal?: AbortSignal) =>
  http.get<Overview>('/api/admin/overview', { signal }).then((r) => r.data)

// ---- admin: servers ----
interface ServerBody {
  name: string
  address?: string
  region?: string
  remark?: string
}
export const listServers = (signal?: AbortSignal) =>
  http.get<{ servers: Server[]; latest_agent_version: string }>('/api/admin/servers', { signal }).then((r) => r.data.servers)
export const getServersMeta = (signal?: AbortSignal) =>
  http.get<{ servers: Server[]; latest_agent_version: string }>('/api/admin/servers', { signal }).then((r) => r.data)
export const createServer = (body: ServerBody) =>
  http.post<{ server: Server; install_command: string; public_url: string }>('/api/admin/servers', body).then((r) => r.data)
export const getServer = (id: number, signal?: AbortSignal) =>
  http.get<{ server: Server; install_command: string; public_url: string }>(`/api/admin/servers/${id}`, { signal }).then((r) => r.data)
export const updateServer = (id: number, body: ServerBody) =>
  http.put(`/api/admin/servers/${id}`, body).then((r) => r.data)
export const updateServerOrder = (ids: number[]) =>
  http.put('/api/admin/servers/order', { ids }).then((r) => r.data)
export const deleteServer = (id: number) => http.delete(`/api/admin/servers/${id}`).then((r) => r.data)
export const installSingbox = (id: number, body: { channel?: string; version?: string; method?: string }) =>
  http.post<{ ok: boolean; output: string }>(`/api/admin/servers/${id}/install-singbox`, body).then((r) => r.data)
export const uninstallSingbox = (id: number) =>
  http.post<{ ok: boolean; output: string }>(`/api/admin/servers/${id}/uninstall-singbox`).then((r) => r.data)
export const uninstallAgent = (id: number) =>
  http.post<{ ok: boolean; output: string }>(`/api/admin/servers/${id}/uninstall-agent`).then((r) => r.data)
export const serviceAction = (id: number, action: string) =>
  http.post<{ ok: boolean; output: string }>(`/api/admin/servers/${id}/service`, { action }).then((r) => r.data)
export const updateAgent = (id: number) =>
  http.post<{ ok: boolean; updated: boolean; output: string }>(`/api/admin/servers/${id}/update-agent`).then((r) => r.data)
export interface AgentUpdateResult {
  server_id: number
  server_name: string
  success: boolean
  output?: string
  error?: string
}
export interface AgentUpdateBatchResult {
  ok: boolean
  message: string
  count: number
  requested: number
  succeeded: number
  failed: number
  results: AgentUpdateResult[]
}
export const updateAllAgents = () =>
  http.post<AgentUpdateBatchResult>('/api/admin/servers/update-all-agents').then((r) => r.data)
export const setConfigMode = (id: number, mode: 'managed') =>
  http.post<{ ok: boolean; config_mode: string }>(`/api/admin/servers/${id}/config-mode`, { mode }).then((r) => r.data)
export const serverLogs = (id: number, lines = 200, signal?: AbortSignal) =>
  http.get<{ text: string }>(`/api/admin/servers/${id}/logs`, { params: { lines }, signal }).then((r) => r.data.text)
export const serverStatus = (id: number, signal?: AbortSignal) =>
  http.get<{ status: StatusData }>(`/api/admin/servers/${id}/status`, { signal }).then((r) => r.data.status)
export const getServerTraffic = (id: number, range: TrafficRange = '24h', signal?: AbortSignal) =>
  http.get<TrafficSeries>(`/api/admin/servers/${id}/traffic`, { params: { range }, signal }).then((r) => r.data)
export const remoteConfig = (id: number, signal?: AbortSignal) =>
  http.get<RemoteConfig>(`/api/admin/servers/${id}/remote-config`, { signal }).then((r) => r.data)
export const applyRawConfig = (id: number, config: string) =>
  http.post<{ ok: boolean; output: string; summary: ImportSummary; config_mode: 'managed' | 'raw' }>(`/api/admin/servers/${id}/apply-raw`, { config }).then((r) => r.data)

// ---- admin: inbounds ----
interface InboundBody {
  type: InboundType
  tag?: string
  listen_port: number
  settings?: InboundSettings
  remark?: string
  enabled?: boolean
}
export const createInbound = (serverId: number, body: InboundBody) =>
  http.post<{ inbound: Inbound } & ConfigApplyResult>(`/api/admin/servers/${serverId}/inbounds`, body).then((r) => r.data)
export const updateInbound = (serverId: number, inboundId: number, body: InboundBody) =>
  http.put<{ inbound: Inbound } & ConfigApplyResult>(`/api/admin/servers/${serverId}/inbounds/${inboundId}`, body).then((r) => r.data)
export const deleteInbound = (serverId: number, inboundId: number) =>
  http.delete<ConfigApplyResult>(`/api/admin/servers/${serverId}/inbounds/${inboundId}`).then((r) => r.data)

// ---- admin: outbounds (landing / transit targets) ----
interface OutboundBody {
  tag: string
  type: OutboundType
  settings?: OutboundSettings
  remark?: string
  sort?: number
}
export const listOutbounds = (serverId: number, signal?: AbortSignal) =>
  http.get<{ outbounds: Outbound[] }>(`/api/admin/servers/${serverId}/outbounds`, { signal }).then((r) => r.data.outbounds)
export const createOutbound = (serverId: number, body: OutboundBody) =>
  http.post<{ outbound: Outbound } & ConfigApplyResult>(`/api/admin/servers/${serverId}/outbounds`, body).then((r) => r.data)
export const updateOutbound = (serverId: number, id: number, body: OutboundBody) =>
  http.put<{ outbound: Outbound } & ConfigApplyResult>(`/api/admin/servers/${serverId}/outbounds/${id}`, body).then((r) => r.data)
export const deleteOutbound = (serverId: number, id: number) =>
  http.delete<ConfigApplyResult>(`/api/admin/servers/${serverId}/outbounds/${id}`).then((r) => r.data)
export interface OutboundTest {
  ok: boolean
  latency_ms: number
  error?: string
  target: string
}
// Reachability of a landing target, measured FROM the node itself.
export const testOutbound = (serverId: number, id: number, signal?: AbortSignal) =>
  http.post<OutboundTest>(`/api/admin/servers/${serverId}/outbounds/${id}/test`, undefined, { signal }).then((r) => r.data)

// ---- admin: route rules (分流) ----
interface RuleBody {
  match?: RuleMatch
  outbound: string
  remark?: string
  sort?: number
  enabled?: boolean
}
export const listRules = (serverId: number, signal?: AbortSignal) =>
  http.get<{ rules: RouteRule[] }>(`/api/admin/servers/${serverId}/rules`, { signal }).then((r) => r.data.rules)
export const createRule = (serverId: number, body: RuleBody) =>
  http.post<{ rule: RouteRule } & ConfigApplyResult>(`/api/admin/servers/${serverId}/rules`, body).then((r) => r.data)
export const updateRule = (serverId: number, id: number, body: RuleBody) =>
  http.put<{ rule: RouteRule } & ConfigApplyResult>(`/api/admin/servers/${serverId}/rules/${id}`, body).then((r) => r.data)
export const deleteRule = (serverId: number, id: number) =>
  http.delete<ConfigApplyResult>(`/api/admin/servers/${serverId}/rules/${id}`).then((r) => r.data)
export const reorderRules = (serverId: number, order: number[]) =>
  http.put<ConfigApplyResult>(`/api/admin/servers/${serverId}/rules/reorder`, { order }).then((r) => r.data)

// ---- admin: rule sets (规则集) ----
export interface RuleSetBody {
  tag: string
  type: 'remote' | 'local'
  format: 'binary' | 'source'
  url?: string
  path?: string
  download_detour?: string
  update_interval?: string
}
export const listRuleSets = (serverId: number, signal?: AbortSignal) =>
  http.get<{ rulesets: RuleSet[] }>(`/api/admin/servers/${serverId}/rulesets`, { signal }).then((r) => r.data.rulesets)
export const createRuleSet = (serverId: number, body: RuleSetBody) =>
  http.post<{ ruleset: RuleSet } & ConfigApplyResult>(`/api/admin/servers/${serverId}/rulesets`, body).then((r) => r.data)
export const updateRuleSet = (serverId: number, id: number, body: RuleSetBody) =>
  http.put<{ ruleset: RuleSet } & ConfigApplyResult>(`/api/admin/servers/${serverId}/rulesets/${id}`, body).then((r) => r.data)
export const deleteRuleSet = (serverId: number, id: number) =>
  http.delete<ConfigApplyResult>(`/api/admin/servers/${serverId}/rulesets/${id}`).then((r) => r.data)

// ---- admin: import an existing config into the panel ----
export interface ImportSummary {
  inbounds: { tag: string; type: string; listen_port: number; single_user: boolean; users: number }[] | null
  outbounds: { tag: string; type: string; info: string }[] | null
  rules: { inbound: string[] | null; outbound: string; info: string }[] | null
  rulesets: { tag: string; type: string; info: string }[] | null
  final: string
  skipped: string[] | null
}
export const importConfig = (serverId: number, opts: { config?: string; dry_run?: boolean }, signal?: AbortSignal) =>
  http
    .post<{ summary: ImportSummary; dry_run?: boolean }>(`/api/admin/servers/${serverId}/import-config`, opts, { signal })
    .then((r) => r.data)

export const setFinalOutbound = (serverId: number, outbound: string) =>
  http.put<ConfigApplyResult>(`/api/admin/servers/${serverId}/final-outbound`, { outbound }).then((r) => r.data)

// ---- admin: log streaming (实时日志推流) ----
export const streamLogs = (serverId: number, body: { enable: boolean; lines?: number; session_id: string }) =>
  http.post<{ ok: boolean; output: string }>(`/api/admin/servers/${serverId}/stream-logs`, body).then((r) => r.data)

// ---- admin: node formats（节点格式：URI / Clash / Surge）----
export interface NodeFormatItem {
  tag: string
  type: string
  name: string
  server: string
  port: number
  params: Record<string, string>
  uri: string
  clash: string
  surge: string
}

export interface NodeFormats {
  items: NodeFormatItem[]
  uri: string
  clash: string
  surge: string
}
export const getNodeFormats = (serverId: number, signal?: AbortSignal) =>
  http.get<NodeFormats>(`/api/admin/servers/${serverId}/node-formats`, { signal }).then((r) => r.data)

// ---- admin: users ----
interface UserBody {
  email?: string
  password?: string
  server_ids?: number[]
  inbound_ids?: number[]
  expire_at?: number | null
  enabled?: boolean
}
export const listUsers = (signal?: AbortSignal) =>
  http.get<{ users: User[] }>('/api/admin/users', { signal }).then((r) => r.data.users)
export const createUser = (body: UserBody) =>
  http.post<{ user: User }>('/api/admin/users', body).then((r) => r.data.user)
export const updateUser = (id: number, body: UserBody) =>
  http.put<{ user: User }>(`/api/admin/users/${id}`, body).then((r) => r.data.user)
export const deleteUser = (id: number) => http.delete(`/api/admin/users/${id}`).then((r) => r.data)

export interface UserAccess {
  user_id: number
  server_ids: number[]
  server_wide: boolean
  inbound_ids: number[]
  custom_node_ids: number[]
  node_order: UserNodeOrderItem[]
}
export interface UserNodeOrderItem {
  node_type: 'managed' | 'custom'
  node_id: number
}
export const getUserAccess = (id: number, signal?: AbortSignal) =>
  http.get<{ access: UserAccess }>(`/api/admin/users/${id}/access`, { signal }).then((r) => r.data.access)
export const updateUserAccess = (id: number, body: Pick<UserAccess, 'server_ids' | 'inbound_ids' | 'custom_node_ids' | 'node_order'>) =>
  http.put<{ access: UserAccess }>(`/api/admin/users/${id}/access`, body).then((r) => r.data.access)

// ---- admin: custom (external) subscription nodes ----
export interface CustomNode {
  id: number
  all_users: boolean
  user_ids: number[]
  excluded_user_ids: number[]
  user_emails?: string[]
  name: string
  group: string
  link: string
  protocol: string
  address: string
  port: number
  params: Record<string, unknown> | null
  enabled: boolean
  sort_order: number
  subscription_id?: number | null
  detail?: {
    protocol: string
    address: string
    port: number
    region?: string
    uri?: string
    params: Record<string, string>
  }
}
export interface CustomNodeBody {
  name?: string
  group?: string
  link?: string
  protocol?: string
  address?: string
  port?: number
  params?: Record<string, unknown> | null
  all_users?: boolean
  user_ids?: number[]
  excluded_user_ids?: number[]
  enabled?: boolean
  sort_order?: number
}
export const listCustomNodes = (signal?: AbortSignal) =>
  http.get<{ nodes: CustomNode[] }>('/api/admin/custom-nodes', { signal }).then((r) => r.data.nodes)
export const createCustomNode = (body: CustomNodeBody) =>
  http.post<{ node: CustomNode }>('/api/admin/custom-nodes', body).then((r) => r.data.node)
export const updateCustomNode = (id: number, body: CustomNodeBody) =>
  http.put<{ node: CustomNode }>(`/api/admin/custom-nodes/${id}`, body).then((r) => r.data.node)
export const deleteCustomNode = (id: number) => http.delete(`/api/admin/custom-nodes/${id}`).then((r) => r.data)
export const batchDeleteCustomNodes = (ids: number[]) =>
  http.post('/api/admin/custom-nodes/batch-delete', { ids }).then((r) => r.data)
export const batchSetCustomNodeGroup = (ids: number[], group: string) =>
  http.post('/api/admin/custom-nodes/batch-group', { ids, group }).then((r) => r.data)

export interface CustomNodeSubscription {
  id: number
  name: string
  url: string
  group: string
  enabled: boolean
  auto_update: boolean
  update_interval_minutes: number
  base_sort_order: number
  name_rewrite_rules: NameRewriteRule[]
  last_sync_at?: string | null
  last_success_at?: string | null
  last_error: string
  source_type: string
  node_count: number
  created_at: string
  updated_at: string
}

export interface NameRewriteRule {
  action?: 'rename' | 'replace_text' | 'exclude_protocol' | 'include_node' | 'exclude_node'
  pattern: string
  replacement: string
  match_mode?: 'text' | 'regexp'
}

export interface CustomNodeSubscriptionBody {
  name: string
  url: string
  group?: string
  enabled?: boolean
  auto_update?: boolean
  update_interval_minutes?: number
  base_sort_order?: number
  name_rewrite_rules?: NameRewriteRule[]
}

export interface CustomNodeSubscriptionSync {
  created: number
  updated: number
  deleted: number
  total: number
  skipped: number
  filtered: number
}

export const listCustomNodeSubscriptions = (signal?: AbortSignal) =>
  http.get<{ subscriptions: CustomNodeSubscription[] }>('/api/admin/custom-node-subscriptions', { signal }).then((r) => r.data.subscriptions)
export const createCustomNodeSubscription = (body: CustomNodeSubscriptionBody) =>
  http
    .post<{ subscription: CustomNodeSubscription; sync: CustomNodeSubscriptionSync; sync_error?: string }>('/api/admin/custom-node-subscriptions', body)
    .then((r) => r.data)
export const updateCustomNodeSubscription = (id: number, body: CustomNodeSubscriptionBody) =>
  http.put<{ subscription: CustomNodeSubscription }>(`/api/admin/custom-node-subscriptions/${id}`, body).then((r) => r.data.subscription)
export const deleteCustomNodeSubscription = (id: number) =>
  http.delete(`/api/admin/custom-node-subscriptions/${id}`).then((r) => r.data)
export const syncCustomNodeSubscription = (id: number) =>
  http
    .post<{ subscription: CustomNodeSubscription; sync: CustomNodeSubscriptionSync }>(`/api/admin/custom-node-subscriptions/${id}/sync`)
    .then((r) => r.data)

// ---- admin: panel maintenance（面板设置：版本更新 + 数据备份）----
export interface MaintenanceInfo {
  current_version: string
  update_supported: boolean
  update_reason?: string
  latest_version?: string
  has_update?: boolean
  latest_error?: string
  db_driver: string
  uptime_seconds: number
}
export const getMaintenanceInfo = (signal?: AbortSignal, refresh = false) =>
  http.get<MaintenanceInfo>('/api/admin/maintenance/info', {
    signal,
    params: refresh ? { refresh: 1 } : undefined,
  }).then((r) => r.data)
export const selfUpdate = (version?: string) =>
  http
    .post<{ ok: boolean; updated: boolean; message: string; version?: string }>(
      '/api/admin/maintenance/update',
      version ? { version } : {},
    )
    .then((r) => r.data)

// downloadBackup fetches the .tar.gz as a blob and triggers a browser download,
// preserving the server-provided filename.
export const downloadBackup = async (): Promise<void> => {
  const resp = await http.get('/api/admin/maintenance/backup', { responseType: 'blob' })
  const dispo = (resp.headers['content-disposition'] as string) || ''
  const m = dispo.match(/filename="?([^"]+)"?/)
  const filename = m ? m[1] : 'singbox-panel-backup.tar.gz'
  const url = URL.createObjectURL(resp.data as Blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  // Safari may not start consuming the blob until after the click handler has
  // returned. Revoking synchronously can therefore cancel an otherwise valid
  // download on iOS and macOS.
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}

// restoreBackup uploads a backup archive; on success the panel restarts to load
// the imported database.
export interface RestoreBackupResult {
  ok: boolean
  restarting: boolean
  secret_applied: boolean
  message: string
}

export const restoreBackup = (file: File) => {
  const fd = new FormData()
  fd.append('file', file)
  return http
    .post<RestoreBackupResult>(
      '/api/admin/maintenance/restore',
      fd,
    )
    .then((r) => r.data)
}

export interface OneDriveBackupFile {
  id: string
  name: string
  size: number
  lastModifiedDateTime: string
}

export interface OneDriveStatus {
  connected: boolean
  auto_sync: boolean
  interval_hours: number
  last_sync_at?: string
  last_backup_name?: string
  last_error?: string
  cloud_error?: string
  folder: string
  backup_name: string
  files: OneDriveBackupFile[]
}

export const getOneDriveStatus = (signal?: AbortSignal) =>
  http.get<OneDriveStatus>('/api/admin/maintenance/onedrive', { signal }).then((r) => r.data)

export interface OneDriveAuthStart {
  session_id: string
  user_code: string
  verification_uri: string
  verification_uri_complete?: string
  message?: string
  interval: number
  expires_in: number
}

export const startOneDriveAuth = (signal?: AbortSignal) =>
  http.post<OneDriveAuthStart>('/api/admin/maintenance/onedrive/auth/start', undefined, { signal }).then((r) => r.data)

export const pollOneDriveAuth = (sessionID: string, signal?: AbortSignal) =>
  http
    .post<{ status: 'pending' | 'connected'; interval?: number }>(
      `/api/admin/maintenance/onedrive/auth/${encodeURIComponent(sessionID)}/poll`,
      undefined,
      { signal },
    )
    .then((r) => r.data)

export const syncOneDriveBackup = () =>
  http
    .post<{ ok: boolean; name: string; message: string }>('/api/admin/maintenance/onedrive/sync')
    .then((r) => r.data)

export const downloadOneDriveBackup = async (id: string, name: string): Promise<void> => {
  const resp = await http.get(`/api/admin/maintenance/onedrive/backups/${encodeURIComponent(id)}/download`, {
    responseType: 'blob',
  })
  const url = URL.createObjectURL(resp.data as Blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}

export const restoreOneDriveBackup = (id: string) =>
  http
    .post<RestoreBackupResult>(`/api/admin/maintenance/onedrive/backups/${encodeURIComponent(id)}/restore`)
    .then((r) => r.data)

export const deleteOneDriveBackup = (id: string) =>
  http.delete<{ ok: boolean }>(`/api/admin/maintenance/onedrive/backups/${encodeURIComponent(id)}`).then((r) => r.data)
