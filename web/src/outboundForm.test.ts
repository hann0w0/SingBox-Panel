import { describe, expect, it } from 'vitest'

import { buildOutboundBody } from './outboundForm'
import type { Outbound } from './types'

describe('buildOutboundBody', () => {
  it('preserves hidden imported parameters while editing visible fields', () => {
    const outbound = {
      id: 1,
      server_id: 1,
      tag: 'trojan-old',
      type: 'trojan',
      remark: '',
      sort: 0,
      settings: {
        server: 'old.example.com',
        server_port: 443,
        password: 'secret',
        settings: {
          tls: {
            enabled: true,
            server_name: 'old.example.com',
            fingerprint: 'firefox',
            reality: { enabled: true, public_key: 'public-key', short_id: ['abcd'] },
          },
          transport: {
            type: 'ws',
            path: '/old',
            headers: { Host: 'old.example.com', Origin: 'https://origin.example.com' },
            max_early_data: 2048,
            early_data_header: 'Sec-WebSocket-Protocol',
          },
        },
      },
    } satisfies Outbound

    const body = buildOutboundBody(outbound, {
      type: 'trojan', tag: 'trojan-new', server: 'new.example.com', server_port: 8443,
      password: 'secret', tls: true, sni: 'sni.example.com', tls_alpn: 'h2', insecure: false,
      transport_type: 'ws', ws_path: '/new', ws_host: 'cdn.example.com',
    })

    expect(body.settings.settings?.tls?.reality?.public_key).toBe('public-key')
    expect(body.settings.settings?.tls?.fingerprint).toBe('firefox')
    expect(body.settings.settings?.transport?.max_early_data).toBe(2048)
    expect(body.settings.settings?.transport?.early_data_header).toBe('Sec-WebSocket-Protocol')
    expect(body.settings.settings?.transport?.headers?.Origin).toBe('https://origin.example.com')
    expect(body.settings.settings?.transport?.headers?.Host).toBe('cdn.example.com')
  })

  it('forces TLS for protocols that require it', () => {
    const body = buildOutboundBody(null, {
      type: 'anytls', tag: 'anytls', server: 'example.com', server_port: 443,
      password: 'secret', tls: false, sni: 'example.com', insecure: false,
    })
    expect(body.settings.settings?.tls?.enabled).toBe(true)
  })

  it('drops old protocol fields when changing the outbound type', () => {
    const outbound = {
      id: 1, server_id: 1, tag: 'old', type: 'trojan', remark: '', sort: 0,
      settings: {
        server: 'old.example.com', server_port: 443, password: 'secret',
        settings: { tls: { enabled: true, reality: { enabled: true, public_key: 'key' } } },
      },
    } satisfies Outbound
    const body = buildOutboundBody(outbound, {
      type: 'socks', tag: 'socks', server: 'proxy.example.com', server_port: 1080,
      username: 'alice', password: 'pw',
    })
    expect(body.settings.settings?.tls).toBeUndefined()
    expect(body.settings.uuid).toBeUndefined()
    expect(body.settings.username).toBe('alice')
  })
})
