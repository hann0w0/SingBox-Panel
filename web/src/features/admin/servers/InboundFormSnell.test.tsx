import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import InboundForm from './InboundForm'
import { updateInbound } from '../../../api'
import type { Inbound, InboundSettings } from '../../../types'

vi.mock('../../../api', () => ({
  createInbound: vi.fn(),
  updateInbound: vi.fn(),
  errMsg: (error: Error) => error.message,
}))
vi.mock('../../../configApply', () => ({
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
  vi.mocked(updateInbound).mockResolvedValue({ apply_state: 'pending' } as Awaited<ReturnType<typeof updateInbound>>)
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  if (originalScrollIntoView) Object.defineProperty(Element.prototype, 'scrollIntoView', originalScrollIntoView)
  else Reflect.deleteProperty(Element.prototype, 'scrollIntoView')
})

function openSnell(settings: InboundSettings) {
  const inbound: Inbound = {
    id: 2, server_id: 1, type: 'snell', tag: 'snell', listen_port: 1443, enabled: true, remark: '',
    created_at: '2026-09-06T00:00:00Z', updated_at: '2026-09-06T00:00:00Z',
    settings: { single_user: true, snell_psk: 'a-long-shared-psk', ...settings },
  }
  render(<InboundForm serverId={1} inbound={inbound} open onClose={vi.fn()} onSaved={vi.fn()} />)
  fireEvent.click(screen.getByText('协议参数'))
}

async function saveSettings() {
  fireEvent.click(screen.getByRole('button', { name: '应用' }))
  await waitFor(() => expect(updateInbound).toHaveBeenCalledOnce())
  const [serverId, inboundId, body] = vi.mocked(updateInbound).mock.calls[0]
  expect([serverId, inboundId]).toEqual([1, 2])
  expect(body.settings?.snell_psk).toBe('a-long-shared-psk')
  return body.settings
}

describe('Snell inbound version switching', () => {
  it.each(['http', 'tls'])('clears v5 %s obfuscation when switching to v6', async (obfs) => {
    openSnell({ snell_version: 5, snell_obfs_mode: obfs })
    fireEvent.click(await screen.findByLabelText('版本'))
    fireEvent.click(await screen.findByRole('option', { name: 'v6' }))
    expect(screen.queryByLabelText('混淆')).toBeNull()
    fireEvent.click(await screen.findByLabelText('模式'))
    fireEvent.click(await screen.findByRole('option', { name: 'unshaped' }))

    expect(await saveSettings()).toMatchObject({
      snell_version: 6, snell_obfs_mode: 'none', snell_mode: 'unshaped',
    })
  })

  it.each(['unshaped', 'unsafe-raw'])('clears v6 %s mode when switching to v5', async (mode) => {
    openSnell({ snell_version: 6, snell_obfs_mode: 'none', snell_mode: mode })
    fireEvent.click(await screen.findByLabelText('版本'))
    fireEvent.click(await screen.findByRole('option', { name: 'v5' }))
    expect(screen.queryByLabelText('模式')).toBeNull()
    fireEvent.click(await screen.findByLabelText('混淆'))
    fireEvent.click(await screen.findByRole('option', { name: 'http' }))

    expect(await saveSettings()).toMatchObject({
      snell_version: 5, snell_obfs_mode: 'http', snell_mode: '',
    })
  })

  it.each([
    { snell_version: 5, snell_obfs_mode: 'tls', snell_mode: '' },
    { snell_version: 6, snell_obfs_mode: 'none', snell_mode: 'unsafe-raw' },
  ])('preserves version-specific settings on an unchanged v$snell_version save', async (settings) => {
    openSnell(settings)
    expect(await saveSettings()).toMatchObject(settings)
  })
})
