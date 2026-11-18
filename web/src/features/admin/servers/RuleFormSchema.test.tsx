import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RuleForm } from './RoutingForms'
import { createRule, updateRule } from '../../../lib/api'
import { DEFAULT_ROUTE_RULE_SCHEMA, validateObjectBySchema } from '../../../lib/singboxSchema'
import type { RouteRule } from '../../../lib/types'

vi.mock('../../../lib/api', () => ({
  createRule: vi.fn(),
  updateRule: vi.fn(),
  createOutbound: vi.fn(),
  updateOutbound: vi.fn(),
  listSharedRuleSets: vi.fn(async () => []),
  errMsg: (e: Error) => e.message,
}))
vi.mock('../../../lib/configApply', () => ({
  applyWithToast: async (_key: string, action: () => Promise<unknown>) => { await action(); return true },
  showConfigApplyResult: vi.fn(),
}))

beforeEach(() => {
  vi.stubGlobal('matchMedia', () => ({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  Element.prototype.scrollIntoView = vi.fn()
  vi.mocked(createRule).mockResolvedValue({ apply_state: 'pending' } as unknown as Awaited<ReturnType<typeof createRule>>)
  vi.mocked(updateRule).mockResolvedValue({ apply_state: 'pending' } as unknown as Awaited<ReturnType<typeof updateRule>>)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

describe('RuleForm JSON Schema integration', () => {
  it('renders and preserves complex schema fields and unknown extra keys in Schema 表单 mode', async () => {
    const complexRule: RouteRule = {
      id: 12,
      server_id: 1,
      sort: 1,
      enabled: true,
      outbound: 'proxy-hk',
      remark: 'complex',
      match: {
        action: 'route',
        domain_regex: ['^api\\..*\\.example\\.com$'],
        clash_mode: 'rule',
        invert: true,
        override_port: 8443,
        wifi_ssid: ['CorpWiFi'],
      },
    }

    render(
      <RuleForm
        serverId={1}
        rule={complexRule}
        outboundTags={['direct', 'proxy-hk']}
        inboundTags={['vless-in']}
        open
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />,
    )

    expect(await screen.findByText('高级匹配与路由参数')).toBeTruthy()
    expect(screen.getByDisplayValue('^api\\..*\\.example\\.com$')).toBeTruthy()
    expect(screen.getByDisplayValue('rule')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(updateRule).toHaveBeenCalledOnce())
    const body = vi.mocked(updateRule).mock.calls[0][2]
    expect(body.outbound).toBe('proxy-hk')
    expect(body.match).toMatchObject({
      action: 'route',
      domain_regex: ['^api\\..*\\.example\\.com$'],
      clash_mode: 'rule',
      invert: true,
      override_port: 8443,
      wifi_ssid: ['CorpWiFi'],
    })
  })

  it('supports JSON 编辑 mode for resolve/complex rules and rejects schema violations', async () => {
    render(
      <RuleForm
        serverId={1}
        rule={null}
        outboundTags={['direct', 'proxy-hk']}
        inboundTags={['vless-in']}
        open
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />,
    )

    fireEvent.click(screen.getByRole('radio', { name: 'JSON 编辑' }))
    const editor = await screen.findByLabelText('rule.json')

    // Invalid schema value: ip_version must be 4 or 6
    fireEvent.change(editor, { target: { value: JSON.stringify({ action: 'route', outbound: 'direct', ip_version: 9 }) } })
    fireEvent.click(screen.getByRole('button', { name: '创建' }))
    expect(await screen.findByText(/不在允许范围内/)).toBeTruthy()
    expect(createRule).not.toHaveBeenCalled()

    // Valid resolve action with server & strategy
    fireEvent.change(editor, {
      target: {
        value: JSON.stringify({
          action: 'resolve',
          inbound: ['vless-in'],
          server: 'dns-remote',
          strategy: 'prefer_ipv4',
          disable_cache: true,
        }),
      },
    })
    fireEvent.click(screen.getByRole('button', { name: '创建' }))

    await waitFor(() => expect(createRule).toHaveBeenCalledOnce())
    const body = vi.mocked(createRule).mock.calls[0][1]
    expect(body.outbound).toBe('resolve')
    expect(body.match).toMatchObject({
      action: 'resolve',
      inbound: ['vless-in'],
      server: 'dns-remote',
      strategy: 'prefer_ipv4',
      disable_cache: true,
    })
  })

  it('validates route rule schema constraints locally', () => {
    expect(validateObjectBySchema(DEFAULT_ROUTE_RULE_SCHEMA, { port: ['not-a-port'] })).toContain('必须是数字')
    expect(validateObjectBySchema(DEFAULT_ROUTE_RULE_SCHEMA, { action: 'route', domain_regex: ['^foo'], invert: true })).toBeNull()
  })
})
