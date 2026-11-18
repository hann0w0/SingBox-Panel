import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import InboundForm from './InboundForm'
import { createInbound, updateInbound } from '../../../lib/api'
import { DEFAULT_INBOUND_SCHEMA, validateObjectBySchema } from '../../../lib/singboxSchema'
import type { Inbound } from '../../../lib/types'

vi.mock('../../../lib/api', () => ({
  createInbound: vi.fn(),
  updateInbound: vi.fn(),
  errMsg: (error: Error) => error.message,
}))
vi.mock('../../../lib/configApply', () => ({
  applyWithToast: vi.fn(async (_key: string, action: () => Promise<unknown>) => {
    await action()
    return true
  }),
}))

const originalScrollIntoView = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollIntoView')

beforeEach(() => {
  vi.stubGlobal('matchMedia', () => ({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  vi.stubGlobal('ResizeObserver', class {
    observe() {}
    unobserve() {}
    disconnect() {}
  })
  Object.defineProperty(Element.prototype, 'scrollIntoView', { configurable: true, writable: true, value: vi.fn() })
  vi.mocked(createInbound).mockResolvedValue({ apply_state: 'pending' } as unknown as Awaited<ReturnType<typeof createInbound>>)
  vi.mocked(updateInbound).mockResolvedValue({ apply_state: 'pending' } as unknown as Awaited<ReturnType<typeof updateInbound>>)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  if (originalScrollIntoView) Object.defineProperty(Element.prototype, 'scrollIntoView', originalScrollIntoView)
  else Reflect.deleteProperty(Element.prototype, 'scrollIntoView')
})

describe('InboundForm JSON Schema integration', () => {
  it('preserves complex raw fields (grpc, tls.min_version, multiplex, tcp_fast_open) when editing in Schema 表单 mode', async () => {
    const complexInbound: Inbound = {
      id: 9,
      server_id: 1,
      type: 'vless',
      tag: 'vless-grpc',
      listen_port: 8443,
      enabled: true,
      remark: '',
      created_at: '2026-11-18T00:00:00Z',
      updated_at: '2026-11-18T00:00:00Z',
      settings: {
        uuid: '11111111-1111-1111-1111-111111111111',
        single_user: true,
        transport: { type: 'grpc' },
        tls: { enabled: true, server_name: 'edge.example.com', certificate_path: '/etc/ssl/cert.pem', key_path: '/etc/ssl/key.pem' },
        raw: {
          type: 'vless',
          tag: 'vless-grpc',
          listen: '::',
          listen_port: 8443,
          tcp_fast_open: true,
          transport: { type: 'grpc', service_name: 'GunService', idle_timeout: '15s' },
          tls: { enabled: true, server_name: 'edge.example.com', min_version: '1.3' },
          multiplex: { enabled: true, padding: true },
        },
      },
    }

    render(<InboundForm serverId={1} inbound={complexInbound} open onClose={vi.fn()} onSaved={vi.fn()} />)

    // gRPC schema field and collapsed 其他参数 group render for complex inbound
    expect(await screen.findByText('其他参数')).toBeTruthy()
    expect(screen.getByDisplayValue('GunService')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: '应用' }))

    await waitFor(() => expect(updateInbound).toHaveBeenCalledOnce())
    const payload = vi.mocked(updateInbound).mock.calls[0][2]
    expect(payload.settings?.raw).toMatchObject({
      tcp_fast_open: true,
      transport: { type: 'grpc', service_name: 'GunService', idle_timeout: '15s' },
      tls: { min_version: '1.3' },
      multiplex: { enabled: true, padding: true },
    })
  })

  it('supports switching to JSON 编辑 mode to create a tun inbound and rejects schema violations', async () => {
    render(<InboundForm serverId={1} inbound={null} open onClose={vi.fn()} onSaved={vi.fn()} />)

    fireEvent.click(screen.getByRole('radio', { name: 'JSON' }))
    const editor = await screen.findByLabelText('inbound.json')

    // Invalid schema: wrong mtu type on tun
    fireEvent.change(editor, { target: { value: JSON.stringify({ type: 'tun', mtu: 'bad-mtu' }) } })
    fireEvent.click(screen.getByRole('button', { name: '应用' }))
    expect(await screen.findByText(/必须是数字/)).toBeTruthy()
    expect(createInbound).not.toHaveBeenCalled()

    // Valid tun config with custom sing-box parameters
    fireEvent.change(editor, {
      target: {
        value: JSON.stringify({
          type: 'tun',
          tag: 'tun-in',
          interface_name: 'tun0',
          address: ['172.19.0.1/30'],
          mtu: 9000,
          auto_route: true,
          strict_route: true,
          stack: 'mixed',
          sniff: true,
        }),
      },
    })
    fireEvent.click(screen.getByRole('button', { name: '应用' }))

    await waitFor(() => expect(createInbound).toHaveBeenCalledOnce())
    const body = vi.mocked(createInbound).mock.calls[0][1]
    expect(body.type).toBe('tun')
    expect(body.tag).toBe('tun-in')
    expect(body.listen_port).toBe(0)
    expect(body.settings?.raw).toMatchObject({
      type: 'tun',
      tag: 'tun-in',
      interface_name: 'tun0',
      mtu: 9000,
      auto_route: true,
      stack: 'mixed',
    })
  })

  it('omits duplicate listen address and legacy fields from inbound 其他参数 UI, and keeps Snell flat without extra drawer', async () => {
    const { rerender } = render(<InboundForm serverId={1} inbound={null} open onClose={vi.fn()} onSaved={vi.fn()} />)

    // Default shadowsocks should show 其他参数 with concise TCP Fast Open / Multiplex, without duplicate 监听地址 or legacy sniff
    expect(await screen.findByText('其他参数')).toBeTruthy()
    expect(screen.queryByText('监听地址')).toBeNull()
    expect(screen.queryByText(/legacy/i)).toBeNull()

    // Snell renders PSK, 版本, 混淆 directly without folding into 其他参数
    const snellInbound: Inbound = {
      id: 3, server_id: 1, type: 'snell', tag: 'snell-direct', listen_port: 10923, enabled: true, remark: '',
      created_at: '2026-11-18T00:00:00Z', updated_at: '2026-11-18T00:00:00Z',
      settings: { snell_version: 5, snell_psk: 'secret-psk', snell_obfs_mode: 'http' },
    }
    rerender(<InboundForm serverId={1} inbound={snellInbound} open onClose={vi.fn()} onSaved={vi.fn()} />)
    expect(screen.getByDisplayValue('secret-psk')).toBeTruthy()
    expect(screen.getByLabelText('版本')).toBeTruthy()
    expect(screen.getByLabelText('混淆')).toBeTruthy()
    expect(screen.queryByText('其他参数')).toBeNull()
  })

  it('validates inbound schema constraints locally', () => {
    expect(validateObjectBySchema(DEFAULT_INBOUND_SCHEMA, { tag: 'x' })).toContain('缺少必填字段 type')
    expect(validateObjectBySchema(DEFAULT_INBOUND_SCHEMA, { type: 'mixed', listen_port: 70000 })).toContain('不能大于 65535')
    expect(validateObjectBySchema(DEFAULT_INBOUND_SCHEMA, { type: 'mixed', listen_port: 1080, set_system_proxy: false })).toBeNull()
  })
})
