import { describe, expect, it } from 'vitest'
import type { CustomNode } from './api'
import { buildCustomNodeParams } from './customNodeForm'

const importedNode: CustomNode = {
  id: 1, name: 'Imported VMess', group: '', link: '', protocol: 'vmess',
  address: 'example.com', port: 443, enabled: true, sort_order: 0,
  all_users: false, user_ids: [], excluded_user_ids: [],
  params: {
    uuid: 'example-uuid', security: 'aes-128-cfb', alter_id: 16,
    tls: 'tls', sni: 'old.example.com', transport: 'ws', path: '/proxy',
    host: 'cdn.example.com', headers: { Host: ['cdn.example.com', 'backup.example.com'], 'X-Test': ['one', 'two'] },
    max_early_data: 2048, early_data_header_name: 'Sec-WebSocket-Protocol', udp: false,
  },
}

describe('buildCustomNodeParams', () => {
  it('preserves imported connection fields on a name-only edit', () => {
    const params = buildCustomNodeParams(importedNode, {
      protocol: 'vmess', name: 'Renamed', uuid: 'example-uuid', tls_mode: 'tls',
      sni: 'old.example.com', transport: 'ws', path: '/proxy', host: 'cdn.example.com',
    })
    expect(params).toEqual(importedNode.params)
    expect(params).not.toBe(importedNode.params)
  })

  it('changes visible values and clears Host without losing unrelated headers', () => {
    const params = buildCustomNodeParams(importedNode, {
      protocol: 'vmess', sni: 'new.example.com', host: '',
    })
    expect(params.sni).toBe('new.example.com')
    expect(params.host).toBe('')
    expect(params.headers).toEqual({ 'X-Test': ['one', 'two'] })
    expect(params.alter_id).toBe(16)
    expect(importedNode.params?.sni).toBe('old.example.com')
  })

  it('does not carry old protocol fields into a new protocol', () => {
    const params = buildCustomNodeParams(importedNode, {
      protocol: 'mixed', username: 'alice', password: 'example-password',
      uuid: 'stale-uuid', tls_mode: 'tls', transport: 'ws',
    })
    expect(params).toEqual({ username: 'alice', password: 'example-password' })
  })

  it('keeps hidden TUIC settings when no corresponding field was rendered', () => {
    const node = { ...importedNode, protocol: 'tuic', params: { uuid: 'id', password: 'pw', alpn: 'h3', heartbeat: '15s', zero_rtt_handshake: true, tls: 'tls' } }
    expect(buildCustomNodeParams(node, { protocol: 'tuic', uuid: 'id', password: 'pw', tls_mode: 'tls' })).toEqual(node.params)
  })

  it('clears incompatible Snell version fields when changing the version', () => {
    const node = { ...importedNode, protocol: 'snell', params: { psk: 'example-password', version: 5, obfs_mode: 'http', obfs_host: 'example.com', reuse: true } }
    expect(buildCustomNodeParams(node, { protocol: 'snell', snell_version: 6, snell_mode: 'unshaped' })).toEqual({ psk: 'example-password', version: 6, mode: 'unshaped', reuse: true })
  })
})
