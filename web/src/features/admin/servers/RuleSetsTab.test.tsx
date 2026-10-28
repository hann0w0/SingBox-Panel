import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RuleSetsTab } from './RuleSetsTab'
import { createRuleSet, updateRuleSet } from '../../../api'
import type { Outbound, RuleSet } from '../../../types'

vi.mock('../../../api', () => ({
  createRuleSet: vi.fn(),
  updateRuleSet: vi.fn(),
  deleteRuleSet: vi.fn(),
  errMsg: (error: Error) => error.message,
}))
vi.mock('../../../configApply', () => ({ showConfigApplyResult: vi.fn() }))

const ruleSet: RuleSet = {
  id: 5, server_id: 1, tag: 'geosite-cn', type: 'remote', format: 'binary',
  url: 'https://example.com/rules.srs', download_detour: '', update_interval: '1d',
}
const outbounds: Outbound[] = ['proxy', '0'].map((tag, index) => ({
  id: index + 1, server_id: 1, tag, type: 'socks', settings: {}, remark: '', sort: index,
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
  vi.mocked(updateRuleSet).mockResolvedValue({ apply_state: 'pending' } as Awaited<ReturnType<typeof updateRuleSet>>)
  vi.mocked(createRuleSet).mockResolvedValue({ apply_state: 'pending' } as Awaited<ReturnType<typeof createRuleSet>>)
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  if (originalScrollIntoView) Object.defineProperty(Element.prototype, 'scrollIntoView', originalScrollIntoView)
  else Reflect.deleteProperty(Element.prototype, 'scrollIntoView')
})

function openEdit(downloadDetour = '') {
  render(<RuleSetsTab serverId={1} ruleSets={[{ ...ruleSet, download_detour: downloadDetour }]} outbounds={outbounds} onRefresh={vi.fn()} />)
  fireEvent.click(screen.getByRole('button', { name: '编辑规则集 geosite-cn' }))
}

async function chooseDetour(label: string) {
  fireEvent.click(await screen.findByLabelText('下载出站'))
  fireEvent.click(await screen.findByRole('option', { name: label }))
}

async function saveEdit() {
  fireEvent.click(screen.getByRole('button', { name: '确定' }))
  await waitFor(() => expect(updateRuleSet).toHaveBeenCalledOnce())
  const [serverId, id, body] = vi.mocked(updateRuleSet).mock.calls[0]
  expect([serverId, id]).toEqual([1, 5])
  return body
}

describe('rule-set download routing', () => {
  it('keeps the default outbound when only the tag is edited', async () => {
    openEdit()
    fireEvent.change(await screen.findByLabelText(/标签/), { target: { value: 'renamed' } })
    expect(await saveEdit()).toMatchObject({ tag: 'renamed', download_detour: '', update_interval: '1d' })
  })

  it('keeps the default outbound when only the update interval is edited', async () => {
    openEdit()
    fireEvent.change(await screen.findByLabelText('更新间隔'), { target: { value: '2d' } })
    expect(await saveEdit()).toMatchObject({ tag: ruleSet.tag, download_detour: '', update_interval: '2d' })
  })

  it.each(['direct', 'proxy', '0'])('preserves the explicit %s outbound on an unrelated edit', async (detour) => {
    openEdit(detour)
    expect((await screen.findByLabelText('下载出站')).textContent).toBe(detour)
    fireEvent.change(screen.getByLabelText('更新间隔'), { target: { value: '2d' } })
    expect(await saveEdit()).toMatchObject({ download_detour: detour, update_interval: '2d' })
  })

  it('lets an explicit direct route switch back to the default outbound', async () => {
    openEdit('direct')
    await chooseDetour('跟随默认出站')
    expect(screen.getByLabelText('下载出站').textContent).toBe('跟随默认出站')
    expect(await saveEdit()).toMatchObject({ download_detour: '' })
  })

  it.each(['direct', 'proxy', '0'])('lets the default outbound switch to explicit %s', async (detour) => {
    openEdit()
    expect((await screen.findByLabelText('下载出站')).textContent).toBe('跟随默认出站')
    await chooseDetour(detour)
    expect(await saveEdit()).toMatchObject({ download_detour: detour })
  })

  it.each([
    { label: 'direct', detour: 'direct' },
    { label: '跟随默认出站', detour: '' },
  ])('creates a remote rule set with $label download routing', async ({ label, detour }) => {
    const props = { serverId: 1, ruleSets: [], outbounds, onRefresh: vi.fn() }
    const { rerender } = render(<RuleSetsTab {...props} />)
    rerender(<RuleSetsTab {...props} createSignal={1} />)
    expect((await screen.findByLabelText('下载出站')).textContent).toBe('direct')
    fireEvent.change(screen.getByLabelText(/标签/), { target: { value: ruleSet.tag } })
    fireEvent.change(screen.getByLabelText(/URL/), { target: { value: ruleSet.url } })
    if (detour !== 'direct') await chooseDetour(label)
    fireEvent.click(screen.getByRole('button', { name: '确定' }))
    await waitFor(() => expect(createRuleSet).toHaveBeenCalledOnce())
    expect(vi.mocked(createRuleSet).mock.calls[0]).toEqual([1, expect.objectContaining({
      tag: ruleSet.tag, url: ruleSet.url, download_detour: detour,
    })])
  })
})
