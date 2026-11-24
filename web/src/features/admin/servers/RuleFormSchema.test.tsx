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

    expect(await screen.findByText('其他参数')).toBeTruthy()
    expect(screen.getByDisplayValue('^api\\..*\\.example\\.com$')).toBeTruthy()
    expect(screen.getAllByDisplayValue('rule').length).toBeGreaterThan(0)

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

    fireEvent.click(screen.getByRole('radio', { name: 'JSON' }))
    const editor = await screen.findByLabelText('rule.json')

    // Invalid schema value: ip_version must be 4 or 6
    fireEvent.change(editor, { target: { value: JSON.stringify({ action: 'route', outbound: 'direct', ip_version: 9 }) } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
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
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

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

  it('surfaces strategy directly in Form mode when action is resolve', async () => {
    render(
      <RuleForm
        serverId={1}
        rule={null}
        outboundTags={['direct']}
        inboundTags={['vless-in']}
        open
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />,
    )

    fireEvent.click(screen.getByRole('combobox', { name: '规则动作' }))
    fireEvent.click(await screen.findByRole('option', { name: 'resolve · 解析域名' }))

    const strategyBox = await screen.findByRole('combobox', { name: '解析策略' })
    fireEvent.click(strategyBox)
    fireEvent.click(await screen.findByRole('option', { name: 'prefer_ipv4 · 优先 IPv4' }))
    await waitFor(() => expect(strategyBox.textContent).toContain('prefer_ipv4'))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(createRule).toHaveBeenCalledOnce())
    const body = vi.mocked(createRule).mock.calls[0][1]
    expect(body.outbound).toBe('resolve')
    expect(body.match).toMatchObject({
      action: 'resolve',
      strategy: 'prefer_ipv4',
    })
  })

  it('surfaces sniff timeout in Form mode and saves it with the sniffer list', async () => {
    render(
      <RuleForm
        serverId={1}
        rule={null}
        outboundTags={['direct']}
        inboundTags={['vless-in']}
        open
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />,
    )

    fireEvent.click(screen.getByRole('combobox', { name: '规则动作' }))
    fireEvent.click(await screen.findByRole('option', { name: 'sniff · 协议嗅探' }))

    fireEvent.click(await screen.findByRole('combobox', { name: '嗅探协议' }))
    fireEvent.click(await screen.findByRole('option', { name: 'TLS' }))
    fireEvent.change(screen.getByLabelText('嗅探超时'), { target: { value: '300ms' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(createRule).toHaveBeenCalledOnce())
    const body = vi.mocked(createRule).mock.calls[0][1]
    expect(body.outbound).toBe('sniff')
    expect(body.match).toMatchObject({
      action: 'sniff',
      sniffer: ['tls'],
      timeout: '300ms',
    })
  })

  it('saves reject no_drop and resolve timeout only for their own actions', async () => {
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

    fireEvent.click(screen.getByRole('combobox', { name: '规则动作' }))
    fireEvent.click(await screen.findByRole('option', { name: 'reject · 拒绝连接' }))
    fireEvent.click(await screen.findByRole('combobox', { name: '条件字段' }))
    fireEvent.click(await screen.findByRole('option', { name: /^ip_is_private/ }))
    fireEvent.click(screen.getByLabelText('禁用自动降级'))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(createRule).toHaveBeenCalledOnce())
    expect(vi.mocked(createRule).mock.calls[0][1].match).toMatchObject({
      action: 'reject',
      no_drop: true,
    })
  })

  it('creates an explicit ip_cidr + port -> direct rule in JSON mode even when action is omitted', async () => {
    render(
      <RuleForm
        serverId={1}
        rule={null}
        outboundTags={['direct']}
        inboundTags={['vless-in']}
        open
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />,
    )

    fireEvent.click(screen.getByRole('radio', { name: 'JSON' }))
    const editor = await screen.findByLabelText('rule.json')
    fireEvent.change(editor, {
      target: {
        value: JSON.stringify({
          ip_cidr: ['127.0.0.1/32'],
          port: [9928],
          outbound: 'direct',
        }),
      },
    })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(createRule).toHaveBeenCalledOnce())
    const body = vi.mocked(createRule).mock.calls[0][1]
    expect(body.outbound).toBe('direct')
    expect(body.match).toMatchObject({
      action: 'route',
      ip_cidr: ['127.0.0.1/32'],
      port: [9928],
    })
  })

  it('rejects empty match conditions for route rules in both form and JSON modes', async () => {
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

    fireEvent.click(screen.getByRole('combobox', { name: '目标出站' }))
    fireEvent.click(await screen.findByRole('option', { name: 'direct' }))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    expect(await screen.findByText(/路由规则至少需要一个匹配条件/)).toBeTruthy()
    expect(createRule).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('radio', { name: 'JSON' }))
    const editor = await screen.findByLabelText('rule.json')
    fireEvent.change(editor, { target: { value: JSON.stringify({ action: 'route', outbound: 'direct' }) } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    expect(await screen.findByText(/路由规则至少需要一个匹配条件/)).toBeTruthy()
    expect(createRule).not.toHaveBeenCalled()
  })

  it('exposes advanced route match and route-options fields in Form mode', async () => {
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

    fireEvent.click(screen.getByRole('combobox', { name: '规则动作' }))
    fireEvent.click(await screen.findByRole('option', { name: 'route · 路由分流' }))

    fireEvent.click(await screen.findByRole('button', { name: '添加参数' }))
    const paramSelects = screen.getAllByRole('combobox', { name: '参数名' })
    expect(paramSelects.length).toBe(1)
    fireEvent.click(paramSelects[0])
    expect(await screen.findByRole('option', { name: /process_name/ })).toBeTruthy()
    expect(screen.getByRole('option', { name: /^tls_fragment\b/ })).toBeTruthy()
  })

  it('keeps invert out of the match-condition section and offers it as an optional extra parameter', async () => {
    render(
      <RuleForm
        serverId={1}
        rule={null}
        outboundTags={['direct']}
        inboundTags={['vless-in']}
        open
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />,
    )

    expect(await screen.findByText('匹配条件')).toBeTruthy()
    expect(screen.queryByRole('switch', { name: '匹配结果取反' })).toBeNull()

    fireEvent.click(await screen.findByRole('button', { name: '添加参数' }))
    fireEvent.click(screen.getByRole('combobox', { name: '参数名' }))
    expect(await screen.findByRole('option', { name: /^invert\b/ })).toBeTruthy()
  })
})
