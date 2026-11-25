import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import InboundForm from './InboundForm'
import { OutboundForm, RuleForm } from './RoutingForms'
import { createInbound, createOutbound, createRule, updateInbound, updateOutbound, updateRule } from '../../../lib/api'
import type { Inbound, Outbound, RouteRule } from '../../../lib/types'
import { Modal } from '../../../components/kit/dialogs'

vi.mock('../../../lib/api', () => ({
  createInbound: vi.fn(),
  updateInbound: vi.fn(),
  createOutbound: vi.fn(),
  updateOutbound: vi.fn(),
  createRule: vi.fn(),
  updateRule: vi.fn(),
  listSharedRuleSets: vi.fn(async () => []),
  errMsg: (e: Error) => e.message,
}))

vi.mock('../../../lib/configApply', () => ({
  applyWithToast: vi.fn(async (_key: string, action: () => Promise<unknown>) => {
    await action()
    return true
  }),
  showConfigApplyResult: vi.fn(),
}))

beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('matchMedia', () => ({
    matches: false,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }))
  vi.stubGlobal('ResizeObserver', class {
    observe() {}
    unobserve() {}
    disconnect() {}
  })
  Element.prototype.scrollIntoView = vi.fn()
  vi.mocked(createInbound).mockResolvedValue({ apply_state: 'pending' } as unknown as Awaited<ReturnType<typeof createInbound>>)
  vi.mocked(updateInbound).mockResolvedValue({ apply_state: 'pending' } as unknown as Awaited<ReturnType<typeof updateInbound>>)
  vi.mocked(createOutbound).mockResolvedValue({ apply_state: 'pending' } as unknown as Awaited<ReturnType<typeof createOutbound>>)
  vi.mocked(updateOutbound).mockResolvedValue({ apply_state: 'pending' } as unknown as Awaited<ReturnType<typeof updateOutbound>>)
  vi.mocked(createRule).mockResolvedValue({ apply_state: 'pending' } as unknown as Awaited<ReturnType<typeof createRule>>)
  vi.mocked(updateRule).mockResolvedValue({ apply_state: 'pending' } as unknown as Awaited<ReturnType<typeof updateRule>>)
})

afterEach(() => {
  cleanup()
  Modal.destroyAll()
  vi.unstubAllGlobals()
})

describe('Node 6 form & schema regressions', () => {
  it('keeps cleared optional extra parameters mounted in InboundForm during the modal session', async () => {
    const inbound: Inbound = {
      id: 10,
      server_id: 1,
      type: 'vless',
      tag: 'vless-in',
      listen_port: 443,
      enabled: true,
      remark: '',
      created_at: '',
      updated_at: '',
      settings: {
        single_user: true,
        uuid: '11111111-1111-1111-1111-111111111111',
        raw: { detour: 'upstream-tag', sniff_timeout: '300ms' },
      },
    }
    render(<InboundForm serverId={1} inbound={inbound} open onClose={vi.fn()} onSaved={vi.fn()} />)
    const detourInput = screen.getByLabelText('前置入站') as HTMLInputElement
    expect(detourInput).toBeTruthy()
    expect(detourInput.value).toBe('upstream-tag')

    fireEvent.change(detourInput, { target: { value: '' } })
    expect(screen.getByLabelText('前置入站')).toBe(detourInput)
    expect(detourInput.value).toBe('')
  })

  it('syncs single-user users[0] between Form and JSON in InboundForm and rejects multi-user JSON in single-user mode', async () => {
    const inbound: Inbound = {
      id: 11,
      server_id: 1,
      type: 'vless',
      tag: 'vless-single',
      listen_port: 443,
      enabled: true,
      remark: '',
      created_at: '',
      updated_at: '',
      settings: {
        single_user: true,
        uuid: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      },
    }
    render(<InboundForm serverId={1} inbound={inbound} open onClose={vi.fn()} onSaved={vi.fn()} />)

    fireEvent.click(screen.getByRole('radio', { name: 'JSON' }))
    const editor = await screen.findByLabelText('inbound.json') as HTMLTextAreaElement
    const parsedFromForm = JSON.parse(editor.value)
    expect(parsedFromForm.users).toEqual([{ uuid: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa' }])

    // Reject multi-user JSON array while single_user stays enabled.
    const multiUserJson = JSON.stringify({
      type: 'vless',
      tag: 'vless-single',
      listen_port: 443,
      users: [
        { uuid: 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb' },
        { uuid: 'cccccccc-cccc-cccc-cccc-cccccccccccc' },
      ],
    }, null, 2)
    fireEvent.change(editor, { target: { value: multiUserJson } })
    fireEvent.click(screen.getByRole('radio', { name: 'Form' }))
    await waitFor(() => expect(screen.queryByLabelText('inbound.json')).toBeTruthy())
    expect(screen.getByText(/当前处于单用户模式/)).toBeTruthy()

    // Edit single user credential in JSON mode and verify save persists edited UUID.
    const updatedJson = JSON.stringify({
      type: 'vless',
      tag: 'vless-single',
      listen_port: 443,
      users: [{ uuid: 'dddddddd-dddd-dddd-dddd-dddddddddddd' }],
    }, null, 2)
    fireEvent.change(editor, { target: { value: updatedJson } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(updateInbound).toHaveBeenCalledTimes(1))
    expect(vi.mocked(updateInbound).mock.calls[0][2].settings?.uuid).toBe('dddddddd-dddd-dddd-dddd-dddddddddddd')
  })

  it('allows adding extra OutboundForm parameters, keeps them when cleared, and validates Go durations', async () => {
    const outbound: Outbound = {
      id: 21,
      server_id: 1,
      tag: 'corp-http',
      type: 'http',
      remark: '',
      sort: 0,
      settings: { server: 'proxy.example.com', server_port: 8443 },
    }
    render(<OutboundForm serverId={1} outbound={outbound} open onClose={vi.fn()} onSaved={vi.fn()} />)

    fireEvent.click(screen.getByRole('combobox', { name: '添加出站参数' }))
    fireEvent.click(await screen.findByRole('option', { name: /connect_timeout|连接超时/ }))

    const timeoutInput = screen.getByLabelText('连接超时') as HTMLInputElement
    expect(timeoutInput).toBeTruthy()

    fireEvent.change(timeoutInput, { target: { value: 'abc' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(screen.getByText(/请输入时长，例如 300ms/)).toBeTruthy())
    expect(updateOutbound).not.toHaveBeenCalled()

    // Clearing the field keeps it mounted and removes error.
    fireEvent.change(timeoutInput, { target: { value: '' } })
    expect(screen.getByLabelText('连接超时')).toBe(timeoutInput)
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(updateOutbound).toHaveBeenCalledTimes(1))
  })

  it('preserves sniff/hijack-dns rule match conditions and blocks invalid ports/int-arrays', async () => {
    const sniffRule: RouteRule = {
      id: 31,
      server_id: 1,
      sort: 1,
      outbound: 'sniff',
      enabled: true,
      remark: '',
      match: {
        action: 'sniff',
        port: [443],
        network: ['tcp'],
        source_ip_cidr: ['10.0.0.0/8'],
      },
    }
    render(
      <RuleForm
        serverId={1}
        rule={sniffRule}
        outboundTags={['proxy']}
        inboundTags={['vless-in']}
        ruleSets={[]}
        open
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />,
    )

    // Verify condition values are visible in Form mode on sniff action.
    expect(screen.getByDisplayValue('443')).toBeTruthy()
    expect(screen.getByDisplayValue('10.0.0.0/8')).toBeTruthy()

    // Invalid integer port blocks switching to JSON instead of emitting [null].
    const portInput = screen.getByDisplayValue('443')
    fireEvent.change(portInput, { target: { value: 'abc' } })
    fireEvent.click(screen.getByRole('radio', { name: 'JSON' }))
    expect(screen.queryByLabelText('rule.json')).toBeNull()
    expect(screen.getByText(/整数/)).toBeTruthy()

    // Restore valid port, save, and verify all conditions are preserved.
    fireEvent.change(portInput, { target: { value: '443, 8443' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(updateRule).toHaveBeenCalledTimes(1))
    expect(vi.mocked(updateRule).mock.calls[0][2].match).toMatchObject({
      action: 'sniff',
      port: [443, 8443],
      network: ['tcp'],
      source_ip_cidr: ['10.0.0.0/8'],
    })
  })
})
