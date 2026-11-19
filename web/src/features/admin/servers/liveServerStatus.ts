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

export function parseServerLiveStatus(msg: SSEMessage): ServerLiveStatus | null {
  if (msg.kind !== 'server_status' || !msg.data || typeof msg.data !== 'object') return null
  const data = msg.data as Partial<ServerLiveStatus>
  if (typeof data.server_id !== 'number' || !Number.isFinite(data.server_id)) return null
  return data as ServerLiveStatus
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
