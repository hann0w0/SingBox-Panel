import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RuleForm } from './RoutingForms'
import RuleSets from './RuleSets'
import { createRule, listRoutingNodes, listRuleSets } from '../../../api'

vi.mock('../../../api', () => ({
  createRule: vi.fn(), updateRule: vi.fn(), createOutbound: vi.fn(), updateOutbound: vi.fn(),
  listRoutingNodes: vi.fn(), listRuleSets: vi.fn(), createRuleSet: vi.fn(), updateRuleSet: vi.fn(), deleteRuleSet: vi.fn(),
  errMsg: (e: Error) => e.message,
}))
vi.mock('../../../configApply', () => ({
  applyWithToast: async (_key: string, action: () => Promise<unknown>) => { await action(); return true },
  showConfigApplyResult: vi.fn(),
}))

beforeEach(() => {
  vi.stubGlobal('matchMedia', () => ({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  Element.prototype.scrollIntoView = vi.fn()
})
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.clearAllMocks() })

describe('global routing data', () => {
  it('shows the node name and saves its stable tag in a routing rule', async () => {
    vi.mocked(createRule).mockResolvedValue({ apply_state: 'pending' } as Awaited<ReturnType<typeof createRule>>)
    render(<RuleForm serverId={1} rule={null} routingNodes={[{ tag: 'node-42', name: '东京节点', type: 'socks' }]} inboundTags={[]} open onClose={vi.fn()} onSaved={vi.fn()} />)
    fireEvent.click(await screen.findByLabelText(/目标出站/))
    fireEvent.click(await screen.findByRole('option', { name: '东京节点' }))
    expect(screen.getByLabelText(/目标出站/).textContent).toBe('东京节点')
    fireEvent.click(screen.getByRole('button', { name: '创建' }))
    await waitFor(() => expect(createRule).toHaveBeenCalledWith(1, expect.objectContaining({ outbound: 'node-42' })))
  })

  it('shows a loading error and retries global data', async () => {
    vi.mocked(listRuleSets).mockRejectedValueOnce(new Error('规则集不可用')).mockResolvedValueOnce([])
    vi.mocked(listRoutingNodes).mockResolvedValue([])
    render(<RuleSets />)
    expect(await screen.findByText('规则集不可用')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    expect(await screen.findByText('暂无规则集')).toBeTruthy()
    expect(listRuleSets).toHaveBeenCalledTimes(2)
  })
})
