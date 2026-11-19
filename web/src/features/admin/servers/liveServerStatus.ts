import type { SSEMessage } from '../../../hooks/useSSE'
import type { Server } from '../../../lib/types'

export interface ServerLiveStatus {
  server_id: number
  online?: boolean
  load1?: number
  mem_used?: number
  mem_total?: number
  uptime?: number
  singbox_active?: boolean
  traffic_available?: boolean
  traffic_upload_rate?: number
  traffic_download_rate?: number
}

function isServerLiveStatus(value: unknown): value is ServerLiveStatus {
  if (!value || typeof value !== 'object') return false
  const data = value as Partial<ServerLiveStatus>
  return typeof data.server_id === 'number' && Number.isFinite(data.server_id)
}

export function parseServerLiveStatus(msg: SSEMessage): ServerLiveStatus | null {
  if (msg.kind !== 'server_status') return null
  return isServerLiveStatus(msg.data) ? msg.data : null
}

export function parseServerLiveStatuses(msg: SSEMessage): ServerLiveStatus[] {
  if (msg.kind === 'server_status') {
    return isServerLiveStatus(msg.data) ? [msg.data] : []
  }
  if (msg.kind === 'server_status_batch' && Array.isArray(msg.data)) {
    return msg.data.filter(isServerLiveStatus)
  }
  return []
}

export function applyServerLiveStatuses(servers: Server[], patches: ServerLiveStatus[]): Server[] {
  if (patches.length === 0) return servers
  const byID = new Map<number, ServerLiveStatus>()
  for (const patch of patches) byID.set(patch.server_id, patch)
  let changed = false
  const next = servers.map((server) => {
    const patch = byID.get(server.id)
    if (!patch) return server
    changed = true
    return mergeServerLiveStatus(server, patch)
  })
  return changed ? next : servers
}

export function mergeServerLiveStatus(server: Server, patch: ServerLiveStatus): Server {
  if (server.id !== patch.server_id) return server
  const next: Server = { ...server }
  if (typeof patch.online === 'boolean') next.online = patch.online
  if (typeof patch.load1 === 'number' && Number.isFinite(patch.load1)) next.load1 = patch.load1
  if (typeof patch.mem_used === 'number' && Number.isFinite(patch.mem_used)) next.mem_used = patch.mem_used
  if (typeof patch.mem_total === 'number' && Number.isFinite(patch.mem_total)) next.mem_total = patch.mem_total
  if (typeof patch.uptime === 'number' && Number.isFinite(patch.uptime)) next.uptime = patch.uptime
  if (typeof patch.singbox_active === 'boolean') next.singbox_active = patch.singbox_active
  if (typeof patch.traffic_available === 'boolean') next.traffic_available = patch.traffic_available
  if (typeof patch.traffic_upload_rate === 'number' && Number.isFinite(patch.traffic_upload_rate)) next.traffic_upload_rate = patch.traffic_upload_rate
  if (typeof patch.traffic_download_rate === 'number' && Number.isFinite(patch.traffic_download_rate)) next.traffic_download_rate = patch.traffic_download_rate
  return next
}
