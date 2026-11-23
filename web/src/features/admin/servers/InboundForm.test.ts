import { describe, expect, it, vi } from 'vitest'
import type { FormInstance } from '../../../components/kit'
import type { Inbound } from '../../../lib/types'
import { assembleSettings, scrollToFirstError, toForm, validateDuration } from './InboundForm'

const inbound = {
  id: 1, server_id: 1, type: 'vmess', tag: 'example', listen_port: 8443, enabled: true, remark: '',
  created_at: '2026-09-06T00:00:00Z', updated_at: '2026-09-06T00:00:00Z',
  settings: {
    uuid: 'example-uuid', vmess_security: 'auto', single_user: true,
    transport: { type: 'ws', path: '/proxy', headers: { host: ['cdn.example.com', 'backup.example.com'], 'X-Test': ['one', 'two'] }, max_early_data: 2048, early_data_header: 'Sec-WebSocket-Protocol' },
  },
} satisfies Inbound

describe('inbound form round trip', () => {
  it.each([
    ['all', '::', false], ['ipv4', '0.0.0.0', false], ['ipv6', '::', true], ['local', '127.0.0.1', false],
  ] as const)('saves and reopens the %s listen mode', (mode, listen, ipv6_only) => {
    const saved = assembleSettings(inbound.settings, { ...toForm(inbound), listen_mode: mode }, inbound.type)
    expect(saved).toMatchObject({ listen, ipv6_only })
    expect(toForm({ ...inbound, settings: saved }).listen_mode).toBe(mode)
  })

  it('retains an imported bind address when other parameters are edited', () => {
    const item = { ...inbound, settings: { ...inbound.settings, listen: '2001:db8::1' } }
    expect(assembleSettings(item.settings, toForm(item), item.type).listen).toBe('2001:db8::1')
  })
  it('keeps an explicit VLESS UDP encoding choice after save and reopen', () => {
    for (const encoding of ['none', 'xudp', 'packetaddr']) {
      const item = { ...inbound, type: 'vless' as const }
      const saved = assembleSettings(item.settings, { ...toForm(item), packet_encoding: encoding }, item.type)
      const reopened = toForm({ ...item, settings: JSON.parse(JSON.stringify(saved)) })
      expect(reopened.packet_encoding).toBe(encoding)
      expect(assembleSettings(saved, reopened, item.type).packet_encoding).toBe(encoding)
    }
  })

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


  it('preserves imported inline PEM when saving the TLS path form', () => {
    const item = {
      ...inbound,
      type: 'vless' as const,
      settings: {
        ...inbound.settings,
        tls: {
          enabled: true,
          server_name: 'example.com',
          certificate: '-----BEGIN CERTIFICATE-----\ninline\n-----END CERTIFICATE-----',
          key: '-----BEGIN PRIVATE KEY-----\ninline\n-----END PRIVATE KEY-----',
        },
      },
    }
    const values = { ...toForm(item), tls_mode: 'tls' }
    const saved = assembleSettings(item.settings, values, item.type)
    expect(saved.tls?.certificate).toContain('BEGIN CERTIFICATE')
    expect(saved.tls?.key).toContain('BEGIN PRIVATE KEY')
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

  it('round-trips a separate subscription client_port without leaking into tun', () => {
    const saved = assembleSettings(inbound.settings, { ...toForm(inbound), client_port: 443 }, inbound.type)
    expect(saved.client_port).toBe(443)
    expect(toForm({ ...inbound, settings: saved }).client_port).toBe(443)

    const cleared = assembleSettings(saved, { ...toForm({ ...inbound, settings: saved }), client_port: undefined }, inbound.type)
    expect(cleared.client_port).toBeUndefined()

    const tunSaved = assembleSettings(saved, { ...toForm({ ...inbound, settings: saved }), client_port: 443 }, 'tun')
    expect(tunSaved.client_port).toBeUndefined()
  })
})

const realityInbound = {
  id: 2, server_id: 1, type: 'vless', tag: 'reality', listen_port: 443, enabled: true, remark: '',
  created_at: '2026-09-06T00:00:00Z', updated_at: '2026-09-06T00:00:00Z',
  settings: {
    uuid: 'example-uuid', single_user: true, flow: 'xtls-rprx-vision', packet_encoding: 'xudp',
    tls: {
      enabled: true,
      server_name: 'www.apple.com',
      fingerprint: 'chrome',
      reality: {
        enabled: true,
        handshake_server: 'www.apple.com',
        handshake_server_port: 443,
        private_key: 'example-private-key',
      },
    },
  },
} satisfies Inbound

describe('REALITY settings round trip', () => {
  it('preserves a distinct imported SNI, key material, and allows clearing the time limit', () => {
    const base = assembleSettings(realityInbound.settings, {
      ...toForm(realityInbound),
      tls_server_name: 'cdn.example.com',
      reality_max_time_difference: '1m',
    }, 'vless')
    expect(base.tls?.reality?.private_key).toBe('example-private-key')
    expect(base.tls?.reality?.max_time_difference).toBe('1m')

    const saved = assembleSettings(base, { ...toForm({ ...realityInbound, settings: base }), reality_max_time_difference: '' }, 'vless')
    expect(saved.tls?.server_name).toBe('cdn.example.com')
    expect(saved.tls?.reality?.handshake_server).toBe('www.apple.com')
    expect(saved.tls?.reality?.max_time_difference).toBeUndefined()
  })

  it('validates Go duration inputs for REALITY time difference', () => {
    for (const value of ['300ms', '2m30s', '1.5s', '.5s', '+300ms', '1μs', '0', '']) {
      expect(validateDuration(value, true)).toBeUndefined()
    }
    expect(validateDuration('0ms')).toContain('大于 0')
    expect(validateDuration('5')).toContain('请输入时长')
    expect(validateDuration('-1s')).toContain('请输入时长')
  })
})

describe('scrollToFirstError', () => {
  it('scrolls the first invalid field into view instead of switching tabs', () => {
    const scrollToField = vi.fn()
    const form = { scrollToField } as unknown as FormInstance
    scrollToFirstError(form, { errorFields: [{ name: ['reality_handshake_server'], errors: ['x'] }, { name: ['acme_domain'], errors: ['y'] }] })
    expect(scrollToField).toHaveBeenCalledTimes(1)
    expect(scrollToField).toHaveBeenCalledWith(['reality_handshake_server'], expect.objectContaining({ block: 'center' }))
  })

  it('reveals collapsed parameter sections before scrolling to an invalid field', () => {
    const group = document.createElement('details')
    group.className = 'fm-group'
    document.body.append(group)
    const scrollToField = vi.fn(() => expect(group.open).toBe(true))
    scrollToFirstError({ scrollToField } as unknown as FormInstance, { errorFields: [{ name: ['uuid'] }] })
    expect(scrollToField).toHaveBeenCalledOnce()
    group.remove()
  })

  it('ignores non-validation errors', () => {
    const scrollToField = vi.fn()
    scrollToFirstError({ scrollToField } as unknown as FormInstance, new Error('network'))
    expect(scrollToField).not.toHaveBeenCalled()
  })
})
