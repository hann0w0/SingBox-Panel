import { describe, expect, it } from 'vitest'
import type { Inbound } from '../../../types'
import { assembleSettings, toForm } from './InboundForm'

const inbound = {
  id: 1, server_id: 1, type: 'vmess', tag: 'example', listen_port: 8443, enabled: true, remark: '',
  created_at: '2026-09-06T00:00:00Z', updated_at: '2026-09-06T00:00:00Z',
  settings: {
    uuid: 'example-uuid', vmess_security: 'auto', single_user: true,
    transport: { type: 'ws', path: '/proxy', headers: { host: ['cdn.example.com', 'backup.example.com'], 'X-Test': ['one', 'two'] }, max_early_data: 2048, early_data_header: 'Sec-WebSocket-Protocol' },
  },
} satisfies Inbound

describe('inbound form round trip', () => {
  it('preserves custom and repeated headers on an unchanged WebSocket form', () => {
    const values = toForm(inbound)
    expect(values.ws_host).toBe('cdn.example.com')
    expect(assembleSettings(inbound.settings, values, inbound.type).transport).toEqual(inbound.settings.transport)
  })

  it('updates or removes Host without dropping the other headers', () => {
    const values = toForm(inbound)
    const changed = assembleSettings(inbound.settings, { ...values, ws_host: 'new.example.com' }, inbound.type)
    expect(changed.transport?.headers).toEqual({ Host: 'new.example.com', 'X-Test': ['one', 'two'] })
    const cleared = assembleSettings(inbound.settings, { ...values, ws_host: '' }, inbound.type)
    expect(cleared.transport?.headers).toEqual({ 'X-Test': ['one', 'two'] })
    expect(inbound.settings.transport.headers.host).toEqual(['cdn.example.com', 'backup.example.com'])
  })

  it('preserves HTTPUpgrade headers and removes transport when switched to TCP', () => {
    const item = { ...inbound, settings: { ...inbound.settings, transport: { ...inbound.settings.transport, type: 'httpupgrade' } } }
    const values = toForm(item)
    expect(assembleSettings(item.settings, values, item.type).transport?.headers).toEqual(item.settings.transport.headers)
    expect(assembleSettings(item.settings, { ...values, transport_type: 'tcp' }, item.type).transport).toBeUndefined()
  })

  it('always submits SOCKS as a fixed single account, including old multi-user rows', () => {
    const legacy = { ...inbound, type: 'socks' as const, settings: { multi_user: true, username: 'alice', password: 'example-password' } }
    expect(toForm(legacy).multi_user).toBe(false)
    const saved = assembleSettings(legacy.settings, { ...toForm(legacy), multi_user: true }, 'socks')
    expect(saved).toMatchObject({ multi_user: false, single_user: true, username: 'alice', password: 'example-password' })
  })

  it('allows clearing both SOCKS credentials for no-auth mode', () => {
    const saved = assembleSettings({ username: 'alice', password: 'example-password' }, { username: '', password: '', multi_user: true }, 'socks')
    expect(saved).toMatchObject({ multi_user: false, single_user: true, username: '', password: '' })
  })
})
