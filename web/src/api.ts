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
  User,
} from './types'

const http = axios.create({ baseURL: '' })

http.interceptors.request.use((config) => {
  const token = localStorage.getItem('singbox-panel_token')
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

http.interceptors.response.use(
  (r) => r,
  (err) => {
    if (err?.response?.status === 401 && !location.pathname.startsWith('/login')) {
      localStorage.removeItem('singbox-panel_token')
      localStorage.removeItem('singbox-panel_user')
      location.href = '/login'
    }
    return Promise.reject(err)
  },
)

// Extract a human-readable error message from an axios error.
export function errMsg(e: unknown): string {
  const anyE = e as { response?: { data?: { error?: string; output?: string } }; message?: string }
  return anyE?.response?.data?.error || anyE?.response?.data?.output || anyE?.message || '请求失败'
}

export interface ConfigApplyResult {
  apply_state: 'applied' | 'pending' | 'failed'
  apply_error?: string
}

// ---- auth ----
export const login = (username: string, password: string) =>
  http.post<{ token: string; user: User }>('/api/auth/login', { username, password }).then((r) => r.data)

// ---- current user ----
export const getMe = () =>
  http
    .get<{ user: User; subscription_url: string }>('/api/user/me')
    .then((r) => r.data)

export interface UserNode {
  name: string
  type: string
  server: string
  port: number
  link: string
  params: Record<string, string>
}
export const getUserNodes = () =>
  http.get<{ nodes: UserNode[] }>('/api/user/nodes').then((r) => r.data.nodes)

export const resetSub = () =>
  http.post<{ sub_token: string; subscription_url: string }>('/api/user/reset-sub').then((r) => r.data)

export const changePassword = (old_password: string, new_password: string) =>
  http.post<{ ok: boolean; token: string }>('/api/user/change-password', { old_password, new_password }).then((r) => r.data)

// ---- admin: overview ----
export const getOverview = () => http.get<Overview>('/api/admin/overview').then((r) => r.data)

// ---- admin: servers ----
interface ServerBody {
  name: string
  address?: string
  region?: string
  remark?: string
}
export const listServers = () =>
  http.get<{ servers: Server[]; latest_agent_version: string }>('/api/admin/servers').then((r) => r.data.servers)
export const getServersMeta = () =>
  http.get<{ servers: Server[]; latest_agent_version: string }>('/api/admin/servers').then((r) => r.data)
export const createServer = (body: ServerBody) =>
  http.post<{ server: Server; install_command: string; public_url: string }>('/api/admin/servers', body).then((r) => r.data)
export const getServer = (id: number) =>
  http.get<{ server: Server; install_command: string; public_url: string }>(`/api/admin/servers/${id}`).then((r) => r.data)
export const updateServer = (id: number, body: ServerBody) =>
  http.put(`/api/admin/servers/${id}`, body).then((r) => r.data)
export const updateServerOrder = (ids: number[]) =>
  http.put('/api/admin/servers/order', { ids }).then((r) => r.data)
export const deleteServer = (id: number) => http.delete(`/api/admin/servers/${id}`).then((r) => r.data)
export const installSingbox = (id: number, body: { channel?: string; version?: string; method?: string }) =>
  http.post<{ ok: boolean; output: string }>(`/api/admin/servers/${id}/install-singbox`, body).then((r) => r.data)
export const uninstallAgent = (id: number) =>
  http.post<{ ok: boolean; output: string }>(`/api/admin/servers/${id}/uninstall-agent`).then((r) => r.data)
export const serviceAction = (id: number, action: string) =>
  http.post<{ ok: boolean; output: string }>(`/api/admin/servers/${id}/service`, { action }).then((r) => r.data)
export const updateAgent = (id: number) =>
  http.post<{ ok: boolean; output: string }>(`/api/admin/servers/${id}/update-agent`).then((r) => r.data)
export const updateAllAgents = () =>
  http.post<{ ok: boolean; message: string; count: number }>('/api/admin/servers/update-all-agents').then((r) => r.data)
export const setConfigMode = (id: number, mode: 'managed') =>
  http.post<{ ok: boolean; config_mode: string }>(`/api/admin/servers/${id}/config-mode`, { mode }).then((r) => r.data)
export const serverLogs = (id: number, lines = 200) =>
  http.get<{ text: string }>(`/api/admin/servers/${id}/logs`, { params: { lines } }).then((r) => r.data.text)
export const serverStatus = (id: number) =>
  http.get<{ status: StatusData }>(`/api/admin/servers/${id}/status`).then((r) => r.data.status)
export const remoteConfig = (id: number) =>
  http.get<RemoteConfig>(`/api/admin/servers/${id}/remote-config`).then((r) => r.data)
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
export const listOutbounds = (serverId: number) =>
  http.get<{ outbounds: Outbound[] }>(`/api/admin/servers/${serverId}/outbounds`).then((r) => r.data.outbounds)
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
export const testOutbound = (serverId: number, id: number) =>
  http.post<OutboundTest>(`/api/admin/servers/${serverId}/outbounds/${id}/test`).then((r) => r.data)

// ---- admin: route rules (分流) ----
interface RuleBody {
  match?: RuleMatch
  outbound: string
  remark?: string
  sort?: number
  enabled?: boolean
}
export const listRules = (serverId: number) =>
  http.get<{ rules: RouteRule[] }>(`/api/admin/servers/${serverId}/rules`).then((r) => r.data.rules)
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
export const listRuleSets = (serverId: number) =>
  http.get<{ rulesets: RuleSet[] }>(`/api/admin/servers/${serverId}/rulesets`).then((r) => r.data.rulesets)
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
export const importConfig = (serverId: number, opts: { config?: string; dry_run?: boolean }) =>
  http
    .post<{ summary: ImportSummary; dry_run?: boolean }>(`/api/admin/servers/${serverId}/import-config`, opts)
    .then((r) => r.data)

export interface EgressTest {
  ok: boolean
  latency_ms: number
  status?: number
  error?: string
  target: string
}
// Latency from the node itself to the public internet (204 probe).
export const testEgress = (serverId: number) =>
  http.post<EgressTest>(`/api/admin/servers/${serverId}/test-egress`).then((r) => r.data)

export const setFinalOutbound = (serverId: number, outbound: string) =>
  http.put<ConfigApplyResult>(`/api/admin/servers/${serverId}/final-outbound`, { outbound }).then((r) => r.data)

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
export const getNodeFormats = (serverId: number) =>
  http.get<NodeFormats>(`/api/admin/servers/${serverId}/node-formats`).then((r) => r.data)

// ---- admin: users ----
interface UserBody {
  email?: string
  password?: string
  server_ids?: number[]
  inbound_ids?: number[]
  expire_at?: number | null
  enabled?: boolean
}
export const listUsers = () => http.get<{ users: User[] }>('/api/admin/users').then((r) => r.data.users)
export const createUser = (body: UserBody) =>
  http.post<{ user: User }>('/api/admin/users', body).then((r) => r.data.user)
export const updateUser = (id: number, body: UserBody) =>
  http.put<{ user: User }>(`/api/admin/users/${id}`, body).then((r) => r.data.user)
export const deleteUser = (id: number) => http.delete(`/api/admin/users/${id}`).then((r) => r.data)

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
export const getMaintenanceInfo = () =>
  http.get<MaintenanceInfo>('/api/admin/maintenance/info').then((r) => r.data)
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
  URL.revokeObjectURL(url)
}

// restoreBackup uploads a backup archive; on success the panel restarts to load
// the imported database.
export const restoreBackup = (file: File) => {
  const fd = new FormData()
  fd.append('file', file)
  return http
    .post<{ ok: boolean; restarting: boolean; secret_applied: boolean; message: string }>(
      '/api/admin/maintenance/restore',
      fd,
    )
    .then((r) => r.data)
}
