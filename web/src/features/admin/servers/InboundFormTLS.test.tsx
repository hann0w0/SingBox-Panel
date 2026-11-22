import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import InboundForm from './InboundForm'
import { createInbound, updateInbound } from '../../../lib/api'
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

describe('InboundForm TLS configuration', () => {
  it('defaults new AnyTLS inbounds to ACME, omits self-signed, and defaults cert-file mode to insecure=true in the flat form', async () => {
    render(<InboundForm serverId={1} inbound={null} open onClose={vi.fn()} onSaved={vi.fn()} />)

    fireEvent.click(screen.getByRole('combobox', { name: /协议/ }))
    fireEvent.click(await screen.findByText('anytls'))

    const acmeRadio = screen.getByRole('radio', { name: 'ACME 自动证书' })
    const certRadio = screen.getByRole('radio', { name: '证书文件' })
    expect(acmeRadio.getAttribute('aria-checked')).toBe('true')
    expect(screen.queryByRole('radio', { name: /自签证书/ })).toBeNull()
    expect(screen.getByLabelText(/^ACME 域名/)).toBeTruthy()
    expect(screen.getByLabelText('ACME 邮箱')).toBeTruthy()

    fireEvent.click(certRadio)
    const certInput = await screen.findByLabelText(/^证书路径/)
    const keyInput = screen.getByLabelText(/^私钥路径/)
    const skipSwitch = screen.getByLabelText('客户端跳过证书校验')
    expect(skipSwitch.getAttribute('aria-checked')).toBe('true')
    expect(skipSwitch.closest('details')).toBeNull()

    fireEvent.change(certInput, { target: { value: '/etc/sing-box/fullchain.pem' } })
    fireEvent.change(keyInput, { target: { value: '/etc/sing-box/privkey.pem' } })
    fireEvent.change(screen.getByLabelText('SNI'), { target: { value: 'anytls.example.com' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(createInbound).toHaveBeenCalledOnce())
    const [, body] = vi.mocked(createInbound).mock.calls[0]
    expect(body.type).toBe('anytls')
    expect(body.settings?.tls).toMatchObject({
      enabled: true,
      certificate_path: '/etc/sing-box/fullchain.pem',
      key_path: '/etc/sing-box/privkey.pem',
      server_name: 'anytls.example.com',
      insecure: true,
    })
  })

  it('preserves explicit insecure=false when editing an existing certificate-file inbound', async () => {
    const existing: Inbound = {
      id: 12,
      server_id: 1,
      type: 'anytls',
      tag: 'anytls-ca',
      listen_port: 443,
      enabled: true,
      remark: '',
      created_at: '2026-11-22T00:00:00Z',
      updated_at: '2026-11-22T00:00:00Z',
      settings: {
        single_user: true,
        password: 'secret',
        tls: {
          enabled: true,
          server_name: 'ca.example.com',
          certificate_path: '/etc/sing-box/fullchain.pem',
          key_path: '/etc/sing-box/privkey.pem',
          insecure: false,
        },
      },
    }

    render(<InboundForm serverId={1} inbound={existing} open onClose={vi.fn()} onSaved={vi.fn()} />)
    const skipSwitch = await screen.findByLabelText('客户端跳过证书校验')
    expect(skipSwitch.getAttribute('aria-checked')).toBe('false')

    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(updateInbound).toHaveBeenCalledOnce())
    expect(vi.mocked(updateInbound).mock.calls[0][2].settings?.tls?.insecure).toBe(false)
  })

  it('keeps legacy self-signed mode editable so existing inbounds can still be opened or migrated', async () => {
    const legacySelf: Inbound = {
      id: 13,
      server_id: 1,
      type: 'anytls',
      tag: 'anytls-legacy',
      listen_port: 8443,
      enabled: true,
      remark: '',
      created_at: '2026-11-22T00:00:00Z',
      updated_at: '2026-11-22T00:00:00Z',
      settings: {
        single_user: true,
        password: 'secret',
        tls: {
          enabled: true,
          self_signed: true,
          server_name: 'legacy.example.com',
          insecure: true,
        },
      },
    }

    render(<InboundForm serverId={1} inbound={legacySelf} open onClose={vi.fn()} onSaved={vi.fn()} />)
    const legacyRadio = await screen.findByRole('radio', { name: '自签证书（旧）' })
    expect(legacyRadio.getAttribute('aria-checked')).toBe('true')
  })
})
