import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RuleForm } from './RoutingForms'
import RuleSets from './RuleSets'
import { createRule, updateRule, listRoutingNodes, listRuleSets } from '../../../api'
import type { RouteRule } from '../../../types'

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
  it.each(['node-42', 'inbound-42'])('shows the node name and saves its stable %s tag in a routing rule', async (tag) => {
    vi.mocked(createRule).mockResolvedValue({ apply_state: 'pending' } as Awaited<ReturnType<typeof createRule>>)
    render(<RuleForm serverId={1} rule={null} routingNodes={[{ tag, name: '东京节点', type: 'socks' }]} inboundTags={[]} open onClose={vi.fn()} onSaved={vi.fn()} />)
    fireEvent.click(await screen.findByLabelText(/目标出站/))
    expect(await screen.findByRole('option', { name: 'Direct · 直出' })).toBeTruthy()
    fireEvent.click(await screen.findByRole('option', { name: '东京节点' }))
    expect(screen.getByLabelText(/目标出站/).textContent).toBe('东京节点')
    fireEvent.click(screen.getByRole('button', { name: '创建' }))
    await waitFor(() => expect(createRule).toHaveBeenCalledWith(1, expect.objectContaining({ outbound: tag })))
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

const sourceRejectRule: RouteRule = {
  id: 8, server_id: 1, sort: -10, enabled: true, outbound: 'block', remark: '禁止 CN 客户端',
  match: { action: 'reject', inbound: ['dmit-in'], rule_set: ['geoip-cn'], rule_set_ip_cidr_match_source: true },
}

function renderRule(rule: RouteRule | null = null) {
  return render(<RuleForm serverId={1} rule={rule} routingNodes={[]} inboundTags={['dmit-in']}
    ruleSets={[{ id: 1, tag: 'geoip-cn', type: 'remote', format: 'binary', url: 'https://example.com/cn.srs' }]}
    open onClose={vi.fn()} onSaved={vi.fn()} />)
}

describe('rule-set IP match direction', () => {
  it('keeps legacy rules on destination IP', async () => {
    renderRule({ ...sourceRejectRule, match: { action: 'reject', rule_set: ['geoip-cn'] } })
    expect((await screen.findByRole('radio', { name: '目标 IP' })).getAttribute('aria-checked')).toBe('true')
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(updateRule).toHaveBeenCalled())
    expect(vi.mocked(updateRule).mock.calls[0][2].match).not.toHaveProperty('rule_set_ip_cidr_match_source')
  })

  it('defaults to destination IP and creates a source-IP rejection for the selected inbound', async () => {
    renderRule()
    expect(screen.queryByLabelText('规则集 IP 匹配方向')).toBeNull()
    fireEvent.click(await screen.findByRole('radio', { name: '拒绝' }))
    fireEvent.click(screen.getByRole('checkbox', { name: '入站来源' }))
    fireEvent.click(screen.getByRole('combobox', { name: /入站来源/ }))
    fireEvent.click(await screen.findByRole('option', { name: 'dmit-in' }))
    fireEvent.click(screen.getByRole('checkbox', { name: '规则集' }))
    expect((await screen.findByRole('radio', { name: '目标 IP' })).getAttribute('aria-checked')).toBe('true')
    fireEvent.click(screen.getByRole('combobox', { name: /规则集/ }))
    fireEvent.click(await screen.findByRole('option', { name: 'geoip-cn' }))
    fireEvent.click(screen.getByRole('radio', { name: '来源 IP' }))
    fireEvent.click(screen.getByRole('button', { name: '创建' }))
    await waitFor(() => expect(createRule).toHaveBeenCalledWith(1, expect.objectContaining({
      outbound: 'block', match: { action: 'reject', method: 'default', inbound: ['dmit-in'], rule_set: ['geoip-cn'], rule_set_ip_cidr_match_source: true },
    })))
  })

  it.each([false, true])('preserves an edited source rule unless destination IP is selected (%s)', async (switchToTarget) => {
    renderRule(sourceRejectRule)
    expect((await screen.findByRole('radio', { name: '来源 IP' })).getAttribute('aria-checked')).toBe('true')
    if (switchToTarget) fireEvent.click(screen.getByRole('radio', { name: '目标 IP' }))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(updateRule).toHaveBeenCalled())
    const match = vi.mocked(updateRule).mock.calls[0][2].match!
    expect(match.rule_set).toEqual(['geoip-cn'])
    expect(match.rule_set_ip_cidr_match_source).toBe(switchToTarget ? undefined : true)
  })

  it('drops the source flag when the rule-set condition is removed', async () => {
    renderRule(sourceRejectRule)
    await screen.findByRole('radio', { name: '来源 IP' })
    fireEvent.click(screen.getByRole('checkbox', { name: '规则集' }))
    expect(screen.queryByLabelText('规则集 IP 匹配方向')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(updateRule).toHaveBeenCalled())
    const match = vi.mocked(updateRule).mock.calls[0][2].match!
    expect(match).not.toHaveProperty('rule_set')
    expect(match).not.toHaveProperty('rule_set_ip_cidr_match_source')
    expect(match.inbound).toEqual(['dmit-in'])
  })
})
