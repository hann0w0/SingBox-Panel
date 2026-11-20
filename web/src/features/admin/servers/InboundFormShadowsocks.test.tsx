import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import InboundForm from './InboundForm'
import { updateInbound } from '../../../lib/api'
import type { Inbound } from '../../../lib/types'
import { ss2022KeyLen } from '../../../lib/util'

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

const chacha = '2022-blake3-chacha20-poly1305'
const aes128 = '2022-blake3-aes-128-gcm'
const aes256 = '2022-blake3-aes-256-gcm'
const originalScrollIntoView = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollIntoView')
beforeEach(() => {
  vi.stubGlobal('matchMedia', () => ({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  vi.stubGlobal('ResizeObserver', class {
    observe() {}
    unobserve() {}
    disconnect() {}
  })
  Object.defineProperty(Element.prototype, 'scrollIntoView', { configurable: true, writable: true, value: vi.fn() })
  vi.mocked(updateInbound).mockResolvedValue({ apply_state: 'pending' } as Awaited<ReturnType<typeof updateInbound>>)
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  if (originalScrollIntoView) Object.defineProperty(Element.prototype, 'scrollIntoView', originalScrollIntoView)
  else Reflect.deleteProperty(Element.prototype, 'scrollIntoView')
})

function openShadowsocks(method: string, multiUser = false, ipv6Available = true) {
  const psk = btoa('a'.repeat(ss2022KeyLen(method)))
  const inbound: Inbound = {
    id: 2, server_id: 1, type: 'shadowsocks', tag: 'ss', listen_port: 8388, enabled: true, remark: '',
    created_at: '2026-09-06T00:00:00Z', updated_at: '2026-09-06T00:00:00Z',
    settings: { method, ss_server_psk: psk, multi_user: multiUser, single_user: !multiUser },
  }
  render(<InboundForm serverId={1} ipv6Available={ipv6Available} inbound={inbound} open onClose={vi.fn()} onSaved={vi.fn()} />)
  return psk
}

async function chooseMethod(method: string) {
  fireEvent.click(await screen.findByLabelText(/加密方式/))
  fireEvent.click(await screen.findByRole('option', { name: method }))
}

async function saveSettings() {
  fireEvent.click(screen.getByRole('button', { name: '保存' }))
  await waitFor(() => expect(updateInbound).toHaveBeenCalledOnce())
  return vi.mocked(updateInbound).mock.calls[0][2].settings
}

describe('Shadowsocks multi-user capability', () => {
  it('hides IPv6 when the server has no available IPv6', () => {
    openShadowsocks(aes128, false, false)
    fireEvent.click(screen.getByLabelText('监听'))
    expect(screen.getAllByRole('option').map(option => option.textContent)).toEqual(['双栈', '本机', 'IPv4'])
  })
  it('does not silently broaden an existing IPv6 listener when IPv6 is unavailable', async () => {
    const inbound: Inbound = {
      id: 2, server_id: 1, type: 'shadowsocks', tag: 'ss', listen_port: 8388, enabled: true, remark: '',
      created_at: '', updated_at: '', settings: { method: aes128, ss_server_psk: btoa('a'.repeat(16)), listen: '::', ipv6_only: true },
    }
    render(<InboundForm serverId={1} ipv6Available={false} inbound={inbound} open onClose={vi.fn()} onSaved={vi.fn()} />)
    expect(screen.getByLabelText('监听').textContent).toContain('IPv6 不可用')
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await screen.findByText('服务器当前没有可用的 IPv6 地址，请选择其他监听方式')
    expect(updateInbound).not.toHaveBeenCalled()
  })
  it('defaults to all addresses and submits an explicitly selected listen mode', async () => {
    openShadowsocks(aes128)
    const control = screen.getByLabelText('监听')
    expect(control.textContent).toContain('双栈')
    fireEvent.click(control)
    expect(screen.getAllByRole('option').map(option => option.textContent)).toEqual(['双栈', '本机', 'IPv4', 'IPv6'])
    fireEvent.click(await screen.findByRole('option', { name: /^IPv6$/ }))
    expect(await saveSettings()).toMatchObject({ listen: '::', ipv6_only: true })
  })
  it.each([aes128, aes256])('lets %s opt in to multi-user mode', async (method) => {
    const psk = openShadowsocks(method)
    fireEvent.click(await screen.findByLabelText('单端口多用户'))
    expect(await saveSettings()).toMatchObject({ method, multi_user: true, single_user: false, ss_server_psk: psk })
  })

  it('keeps ChaCha valid as single-user without offering multi-user opt-in', async () => {
    const psk = openShadowsocks(chacha)
    expect(screen.queryByLabelText('单端口多用户')).toBeNull()
    expect(await saveSettings()).toMatchObject({ method: chacha, multi_user: false, single_user: true, ss_server_psk: psk })
  })

  it('still generates a 32-byte ChaCha key', async () => {
    openShadowsocks(chacha)
    fireEvent.click(screen.getByRole('button', { name: '随机' }))
    const settings = await saveSettings()
    expect(atob(settings?.ss_server_psk || '').length).toBe(32)
    expect(settings).toMatchObject({ method: chacha, multi_user: false, single_user: true })
  })

  it('clears multi-user mode with a visible explanation when switching AES to ChaCha', async () => {
    const psk = openShadowsocks(aes256, true)
    await chooseMethod(chacha)
    expect(screen.queryByLabelText('单端口多用户')).toBeNull()
    expect(screen.getByText(/仅支持单用户共享密码/)).toBeDefined()
    expect(await saveSettings()).toMatchObject({ method: chacha, multi_user: false, single_user: true, ss_server_psk: psk })
  })

  it('does not resurrect a hidden multi-user flag after returning to AES', async () => {
    openShadowsocks(aes256, true)
    await chooseMethod(chacha)
    await chooseMethod(aes256)
    expect((await screen.findByLabelText('单端口多用户')).getAttribute('aria-checked')).toBe('false')
    expect(await saveSettings()).toMatchObject({ method: aes256, multi_user: false, single_user: true })
  })

  it('blocks unrelated edits to an invalid saved ChaCha mode until explicitly disabled', async () => {
    const psk = openShadowsocks(chacha, true)
    expect(screen.getByText(/当前多用户配置无效/)).toBeDefined()
    expect((await screen.findByLabelText('单端口多用户')).getAttribute('aria-checked')).toBe('true')
    fireEvent.change(screen.getByLabelText('标签'), { target: { value: 'renamed' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await screen.findByRole('alert')
    expect(updateInbound).not.toHaveBeenCalled()

    fireEvent.click(screen.getByLabelText('单端口多用户'))
    expect(screen.queryByLabelText('单端口多用户')).toBeNull()
    expect(await saveSettings()).toMatchObject({ method: chacha, multi_user: false, single_user: true, ss_server_psk: psk })
  })

  it('repairs an invalid saved ChaCha mode by selecting AES-256 without changing the key', async () => {
    const psk = openShadowsocks(chacha, true)
    await chooseMethod(aes256)
    expect((await screen.findByLabelText('单端口多用户')).getAttribute('aria-checked')).toBe('true')
    expect(await saveSettings()).toMatchObject({ method: aes256, multi_user: true, single_user: false, ss_server_psk: psk })
  })

  it('regenerates an incompatible SS2022 key when switching between 16-byte and 32-byte methods', async () => {
    const psk16 = openShadowsocks(aes128, false)
    await chooseMethod(aes256)
    const saved = await saveSettings()
    expect(saved?.method).toBe(aes256)
    expect(saved?.ss_server_psk).not.toBe(psk16)
    expect(atob(saved?.ss_server_psk || '').length).toBe(32)
  })
})
