import { describe, expect, it } from 'vitest'

import { buildOutboundBody } from './outboundForm'
import { inboundSettingsToNativeJson, nativeJsonToInboundSettings } from './singboxSchema'
import type { Outbound } from './types'

describe('buildOutboundBody', () => {
  it('clears both copies of authentication from a URI-imported SOCKS outbound', () => {
    const outbound = {
      id: 1, server_id: 1, tag: 'socks', type: 'socks', remark: '', sort: 0,
      settings: { server: 'node.example.com', server_port: 1080, username: 'alice', password: 'old',
        settings: { username: 'alice', password: 'old' } },
    } satisfies Outbound
    const body = buildOutboundBody(outbound, {
      type: 'socks', tag: 'socks', server: 'node.example.com', server_port: 1080, username: '', password: '',
    })
    expect(body.settings.username).toBe('')
    expect(body.settings.password).toBe('')
    expect(body.settings.settings?.username).toBeUndefined()
    expect(body.settings.settings?.password).toBeUndefined()
    expect(outbound.settings.settings.password).toBe('old')
  })

  it('replaces the PSK of an imported Snell outbound without retaining a hidden override', () => {
    const outbound = {
      id: 1, server_id: 1, tag: 'snell', type: 'snell', remark: '', sort: 0,
      settings: { server: 'node.example.com', server_port: 443, password: 'old-psk',
        settings: { snell_psk: 'old-psk', snell_version: 4, snell_reuse: true } },
    } satisfies Outbound
    const body = buildOutboundBody(outbound, {
      type: 'snell', tag: 'snell', server: 'node.example.com', server_port: 443,
      password: 'new-psk', snell_version: 4, snell_reuse: true,
    })
    expect(body.settings.password).toBe('new-psk')
    expect(body.settings.settings?.snell_psk).toBeUndefined()
    expect(body.settings.settings?.snell_reuse).toBe(true)
    expect(outbound.settings.settings.snell_psk).toBe('old-psk')
  })

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


  it('preserves a case-insensitive Host header when the compact form omits it', () => {
    const outbound = {
      id: 1, server_id: 1, tag: 'vmess', type: 'vmess', remark: '', sort: 0,
      settings: {
        server: 'old.example.com', server_port: 443,
        settings: { transport: { type: 'ws', path: '/ws', headers: { host: 'cdn.example.com', Origin: 'https://origin.example.com' } } },
      },
    } satisfies Outbound

    const body = buildOutboundBody(outbound, {
      type: 'vmess', tag: 'vmess', server: 'new.example.com', server_port: 8443,
      uuid: 'uuid', vmess_security: 'auto', vmess_alter_id: 0,
      transport_type: 'ws', ws_path: '/ws',
    })

    expect(body.settings.settings?.transport?.headers?.host).toBe('cdn.example.com')
    expect(body.settings.settings?.transport?.headers?.Origin).toBe('https://origin.example.com')
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


it('preserves a generic TLS fingerprint for a trojan outbound', () => {
  const outbound = {
    id: 1, server_id: 1, tag: 'trojan', type: 'trojan', remark: '', sort: 0,
    settings: {
      server: 'old.example.com', server_port: 443, password: 'secret',
      settings: { tls: { enabled: true, server_name: 'old.example.com', fingerprint: 'firefox' } },
    },
  } satisfies Outbound
  const body = buildOutboundBody(outbound, {
    type: 'trojan', tag: 'trojan', server: 'new.example.com', server_port: 8443,
    password: 'secret', tls: true, sni: 'new.example.com', tls_fingerprint: 'safari',
  })
  expect(body.settings.settings?.tls?.fingerprint).toBe('safari')
})

it('encodes non-Latin1 HTTP password-only auth safely and keeps it when extraRaw is passed', () => {
  const body = buildOutboundBody(null, {
    type: 'http', tag: 'http-cn', server: 'proxy.example.com', server_port: 8080,
    username: '', password: '密码🚀',
  }, { path: '/proxy', headers: { 'X-Custom': '1' } })
  expect(body.settings.settings?.http_headers?.['X-Custom']).toBe('1')
  expect(body.settings.settings?.http_headers?.['Proxy-Authorization']).toMatch(/^Basic /)
})

it('preserves HTTP and gRPC outbound transport fields and drops stale WS keys when switching', () => {
  const httpBody = buildOutboundBody(null, {
    type: 'vless', tag: 'vless-h2', server: 'h2.example.com', server_port: 443,
    uuid: '11111111-2222-4333-8444-555555555555', tls_mode: 'tls', sni: 'h2.example.com',
    transport_type: 'http',
  }, {
    transport: { type: 'http', path: '/h2', host: ['h2.example.com'], headers: { 'X-Test': 'ok' } },
  })
  expect(httpBody.settings.settings?.transport?.type).toBe('http')
  expect(httpBody.settings.settings?.transport?.path).toBe('/h2')
  expect(httpBody.settings.settings?.transport?.host).toEqual(['h2.example.com'])
  expect((httpBody.settings.settings?.raw?.transport as Record<string, unknown>)?.path).toBe('/h2')
  expect((httpBody.settings.settings?.raw?.transport as Record<string, unknown>)?.headers).toEqual({ 'X-Test': 'ok' })

  const oldWs: Outbound = {
    id: 2, server_id: 1, tag: 'vless-ws', type: 'vless', remark: '', sort: 0,
    settings: {
      server: 'old.example.com', server_port: 443, uuid: '11111111-2222-4333-8444-555555555555',
      settings: {
        transport: { type: 'ws', path: '/ws', headers: { Host: 'old.example.com' }, max_early_data: 2048 },
        raw: { type: 'vless', transport: { type: 'ws', path: '/ws', headers: { Host: 'old.example.com' }, max_early_data: 2048 } },
      },
    },
  }
  const grpcBody = buildOutboundBody(oldWs, {
    type: 'vless', tag: 'vless-grpc', server: 'grpc.example.com', server_port: 443,
    uuid: '11111111-2222-4333-8444-555555555555', tls_mode: 'tls', sni: 'grpc.example.com',
    transport_type: 'grpc',
  }, {
    transport: { type: 'grpc', service_name: 'gunSvc' },
  })
  expect(grpcBody.settings.settings?.transport).toEqual({ type: 'grpc', service_name: 'gunSvc' })
  expect(grpcBody.settings.settings?.raw?.transport).toEqual({ type: 'grpc', service_name: 'gunSvc' })
})

it('syncs TUIC udp_relay_mode from schema extra fields and preserves inline inbound TLS PEM across JSON mode', () => {
  const tuicBody = buildOutboundBody(null, {
    type: 'tuic', tag: 'tuic-out', server: 'tuic.example.com', server_port: 443,
    uuid: '11111111-2222-4333-8444-555555555555', password: 'secret', congestion_control: 'bbr',
    tls: true, sni: 'tuic.example.com',
  }, {
    udp_relay_mode: 'quic',
  })
  expect(tuicBody.settings.settings?.tuic_udp_relay_mode).toBe('quic')
  expect(tuicBody.settings.settings?.raw?.udp_relay_mode).toBe('quic')

  const inlineSettings = {
    tls: {
      enabled: true,
      self_signed: true,
      server_name: 'self.example.com',
      certificate: '-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----',
      key: '-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----',
    },
  }
  const nativeJson = inboundSettingsToNativeJson('hysteria2', 'hy2-in', 443, inlineSettings)
  const tlsJson = nativeJson.tls as Record<string, unknown>
  expect(tlsJson.certificate).toContain('BEGIN CERTIFICATE')
  expect(tlsJson.key).toContain('BEGIN PRIVATE KEY')

  const roundtripped = nativeJsonToInboundSettings(inlineSettings, nativeJson)
  expect(roundtripped.tls?.certificate).toBe(inlineSettings.tls.certificate)
  expect(roundtripped.tls?.key).toBe(inlineSettings.tls.key)
  expect(roundtripped.tls?.self_signed).toBe(true)
})
