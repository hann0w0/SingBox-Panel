import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { OutboundImport } from './OutboundImport'
import { OutboundForm } from './RoutingForms'
import { createOutbound, listOutboundSources, previewOutboundImport } from '../../../api'
import type { OutboundBody, OutboundSource } from '../../../api'

vi.mock('../../../api', () => ({
  createOutbound: vi.fn(), previewOutboundImport: vi.fn(), listOutboundSources: vi.fn(),
  errMsg: (e: Error) => e.message,
}))
vi.mock('../../../configApply', () => ({ showConfigApplyResult: vi.fn() }))
const draft: OutboundBody = {
  tag: 'landing', type: 'trojan',
  settings: { server: 'node.example.com', server_port: 443, password: 'secret', settings: {
    tls: { enabled: true, server_name: 'cdn.example.com', fingerprint: 'firefox' },
    transport: { type: 'ws', path: '/ws', max_early_data: 2048, headers: { Origin: 'https://cdn.example.com' } },
  } },
}
const sources: OutboundSource[] = [
  { kind: 'inbound', id: 11, name: '香港 - vless-hk', group: '香港', type: 'vless', server: 'hk.example.com', server_port: 443 },
  { kind: 'inbound', id: 12, name: '香港 - trojan-hk', group: '香港', type: 'trojan', server: 'hk.example.com', server_port: 8443 },
  { kind: 'inbound', id: 13, name: '自身节点', group: '本机', type: 'vless', disabled: true, unavailable_reason: '不能把节点自身作为出站' },
  { kind: 'custom_node', id: 21, name: '机场节点 A', group: '机场', type: 'hysteria2', server: 'a.example.net', server_port: 443 },
]
const originalScrollIntoView = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollIntoView')
beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('matchMedia', () => ({ matches: false, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  // jsdom has no layout engine; Radix/cmdk still subscribe to resize and
  // request that the active option scroll into view.
  vi.stubGlobal('ResizeObserver', class {
    observe() {}
    unobserve() {}
    disconnect() {}
  })
  Object.defineProperty(Element.prototype, 'scrollIntoView', { configurable: true, writable: true, value: vi.fn() })
  vi.mocked(listOutboundSources).mockResolvedValue(sources)
  vi.mocked(previewOutboundImport).mockResolvedValue(draft)
  vi.mocked(createOutbound).mockResolvedValue({ apply_state: 'pending' } as Awaited<ReturnType<typeof createOutbound>>)
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  if (originalScrollIntoView) Object.defineProperty(Element.prototype, 'scrollIntoView', originalScrollIntoView)
  else Reflect.deleteProperty(Element.prototype, 'scrollIntoView')
})
const saveButton = () => screen.getByRole('button', { name: '保存出站' }) as HTMLButtonElement
const parseURI = () => {
  fireEvent.change(screen.getByLabelText('节点 URI'), { target: { value: 'trojan://secret@node.example.com:443' } })
  fireEvent.click(screen.getByRole('button', { name: '解析链接' }))
}
const expandGroup = async (name: string, group: string) => {
  const list = await screen.findByLabelText(name)
  fireEvent.click(within(list).getByRole('button', { name: new RegExp(`展开 ${group}`) }))
  await waitFor(() => expect(within(list).getAllByRole('option').length).toBeGreaterThan(0))
  return list
}
const chooseSource = async (label: string, name: '面板节点' | '外部节点' = '面板节点', group = '香港') => {
  const list = await expandGroup(name, group)
  fireEvent.click(within(list).getByRole('option', { name: new RegExp(label) }))
}

describe('outbound node imports', () => {
  it('lists panel nodes and imports the chosen one as a followed source', async () => {
    const onClose = vi.fn(), onSaved = vi.fn()
    render(<OutboundImport serverId={3} mode="panel" onClose={onClose} onSaved={onSaved} />)
    await waitFor(() => expect(listOutboundSources).toHaveBeenCalledWith(3, expect.anything()))
    expect(saveButton().disabled).toBe(true)
    await chooseSource('香港 - vless-hk')
    await waitFor(() => expect(previewOutboundImport).toHaveBeenCalledWith(3, { source_kind: 'inbound', source_id: 11 }, expect.anything()))
    await screen.findByLabelText('出站标签')
    fireEvent.click(saveButton())
    await waitFor(() => expect(createOutbound).toHaveBeenCalledWith(3, { ...draft, tag: 'landing', source_kind: 'inbound', source_id: 11 }))
    expect(onSaved).toHaveBeenCalledOnce()
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('reuses the selected external node', async () => {
    render(<OutboundImport serverId={3} mode="external" onClose={vi.fn()} onSaved={vi.fn()} />)
    expect(await screen.findByLabelText('外部节点')).toBeTruthy()
    await chooseSource('机场节点 A', '外部节点', '机场')
    await waitFor(() => expect(previewOutboundImport).toHaveBeenCalledWith(3, { source_kind: 'custom_node', source_id: 21 }, expect.anything()))
  })

  it('disables a node that cannot be its own outbound', async () => {
    render(<OutboundImport serverId={3} mode="panel" onClose={vi.fn()} onSaved={vi.fn()} />)
    const list = await expandGroup('面板节点', '本机')
    const option = await within(list).findByRole('option', { name: /自身节点/ })
    expect(option.getAttribute('aria-disabled')).toBe('true')
    expect((option as HTMLButtonElement).disabled).toBe(true)
  })

  it('keeps groups collapsed until expanded', async () => {
    render(<OutboundImport serverId={3} mode="panel" onClose={vi.fn()} onSaved={vi.fn()} />)
    const list = await screen.findByLabelText('面板节点')
    expect(within(list).getByText('香港')).toBeTruthy()
    expect(within(list).getByText('本机')).toBeTruthy()
    // Collapsed by default: no node rows yet.
    expect(within(list).queryAllByRole('option')).toHaveLength(0)
    fireEvent.click(within(list).getByRole('button', { name: /展开 香港/ }))
    expect(within(list).getAllByRole('option')).toHaveLength(2)
    // Addresses stay hidden in this picker.
    expect(within(list).queryByText(/hk\.example\.com/)).toBeNull()
    fireEvent.click(within(list).getByRole('button', { name: /收起 香港/ }))
    expect(within(list).queryAllByRole('option')).toHaveLength(0)
  })

  it('saves the complete parsed configuration and allows renaming', async () => {
    const onClose = vi.fn(), onSaved = vi.fn()
    render(<OutboundImport serverId={3} mode="uri" onClose={onClose} onSaved={onSaved} />)
    expect(saveButton().disabled).toBe(true)
    parseURI()
    await screen.findByLabelText('出站标签')
    fireEvent.change(screen.getByLabelText('出站标签'), { target: { value: 'renamed' } })
    fireEvent.click(saveButton())
    await waitFor(() => expect(createOutbound).toHaveBeenCalledWith(3, { ...draft, tag: 'renamed' }))
    expect(onSaved).toHaveBeenCalledOnce()
    expect(onClose).toHaveBeenCalledOnce()
  })
  it('discards an in-flight preview when the URI changes', async () => {
    let resolve!: (value: OutboundBody) => void
    vi.mocked(previewOutboundImport).mockReturnValue(new Promise((done) => { resolve = done }))
    render(<OutboundImport serverId={3} mode="uri" onClose={vi.fn()} onSaved={vi.fn()} />)
    parseURI()
    fireEvent.change(screen.getByLabelText('节点 URI'), { target: { value: 'socks5://new.example.com:1080' } })
    await act(async () => resolve(draft))
    expect(screen.queryByLabelText('出站标签')).toBeNull()
    expect(saveButton().disabled).toBe(true)
    expect(createOutbound).not.toHaveBeenCalled()
  })
  it('keeps the draft and displays the error when saving fails', async () => {
    vi.mocked(createOutbound).mockRejectedValue(new Error('标签已存在'))
    const onClose = vi.fn()
    render(<OutboundImport serverId={3} mode="uri" onClose={onClose} onSaved={vi.fn()} />)
    parseURI()
    await screen.findByLabelText('出站标签')
    fireEvent.click(saveButton())
    await screen.findByText('标签已存在')
    expect((screen.getByLabelText('出站标签') as HTMLInputElement).value).toBe('landing')
    expect(onClose).not.toHaveBeenCalled()
    expect(saveButton().disabled).toBe(false)
  })
  it('does not close or unlock a replacement dialog after an old save completes', async () => {
    let resolve!: (value: Awaited<ReturnType<typeof createOutbound>>) => void
    vi.mocked(createOutbound).mockReturnValue(new Promise((done) => { resolve = done }))
    const onClose = vi.fn(), onSaved = vi.fn(), onSavingChange = vi.fn()
    const { unmount } = render(<OutboundImport serverId={3} mode="uri" onClose={onClose} onSaved={onSaved} onSavingChange={onSavingChange} />)
    parseURI()
    await screen.findByLabelText('出站标签')
    fireEvent.click(saveButton())
    await waitFor(() => expect(createOutbound).toHaveBeenCalledOnce())
    unmount()
    await act(async () => resolve({ apply_state: 'pending' } as Awaited<ReturnType<typeof createOutbound>>))
    expect(onClose).not.toHaveBeenCalled()
    expect(onSaved).not.toHaveBeenCalled()
    expect(onSavingChange).toHaveBeenCalledExactlyOnceWith(true)
  })
  it('submits an import from the fixed modal footer', async () => {
    const onClose = vi.fn(), onSaved = vi.fn()
    render(<OutboundForm serverId={3} outbound={null} open onClose={onClose} onSaved={onSaved} />)
    fireEvent.click(screen.getByRole('tab', { name: 'URI 导入' }))
    expect(saveButton().disabled).toBe(true)
    expect(saveButton().closest('.ui-modal-footer')).not.toBeNull()
    parseURI()
    await screen.findByLabelText('出站标签')
    await waitFor(() => expect(saveButton().disabled).toBe(false))
    fireEvent.click(saveButton())
    await waitFor(() => expect(createOutbound).toHaveBeenCalledWith(3, draft))
    expect(onSaved).toHaveBeenCalledOnce()
    expect(onClose).toHaveBeenCalledOnce()
  })
})
