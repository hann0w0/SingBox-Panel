import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createSharedRuleSet, listServers, listSharedRuleSets, updateSharedRuleSet } from '../../../lib/api'
import type { Server } from '../../../lib/types'
import RuleSetsPage from './RuleSetsPage'
import { message } from '../../../components/kit/feedback'

vi.mock('../../../lib/api', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../../lib/api')>(),
  listSharedRuleSets: vi.fn(),
  listServers: vi.fn(),
  updateSharedRuleSet: vi.fn(),
  createSharedRuleSet: vi.fn(),
}))

beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('innerWidth', 1024)
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  Object.defineProperty(Element.prototype, 'scrollIntoView', { configurable: true, writable: true, value: vi.fn() })
  vi.mocked(listSharedRuleSets).mockResolvedValue([{
    id: 1, tag: 'geosite-cn', type: 'remote', format: 'binary',
    url: 'https://example.com/cn.srs', server_ids: [2],
  }])
  vi.mocked(listServers).mockResolvedValue([{ id: 2, name: 'Tokyo' } as Awaited<ReturnType<typeof listServers>>[number]])
})

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

it('shows referenced servers without a manual assignment action', async () => {
  render(<MemoryRouter><RuleSetsPage /></MemoryRouter>)
  expect(await screen.findByText('geosite-cn')).toBeTruthy()
  expect(screen.getByText('Tokyo')).toBeTruthy()
  expect(screen.queryByRole('button', { name: '分配' })).toBeNull()
  expect(screen.getByRole('button', { name: '编辑规则集 geosite-cn' })).toBeTruthy()
})

it('shows field validation without a generic error toast and reports real save failures', async () => {
  const errorToast = vi.spyOn(message, 'error')
  vi.mocked(createSharedRuleSet).mockRejectedValueOnce(new Error('保存失败'))
  render(<MemoryRouter><RuleSetsPage /></MemoryRouter>)
  await screen.findByText('geosite-cn')
  fireEvent.click(screen.getByRole('button', { name: '新增规则集' }))
  const dialog = screen.getByRole('dialog', { name: '新增规则集' })
  fireEvent.click(within(dialog).getByRole('button', { name: '确定' }))
  await screen.findByText('请输入规则集标签')
  await screen.findByText('请输入远程规则集下载 URL')
  expect(errorToast).not.toHaveBeenCalled()
  expect(createSharedRuleSet).not.toHaveBeenCalled()
  fireEvent.change(within(dialog).getByRole('textbox', { name: /标签/ }), { target: { value: 'test-rules' } })
  fireEvent.change(within(dialog).getByRole('textbox', { name: /URL/ }), { target: { value: 'https://example.com/test.srs' } })
  fireEvent.click(within(dialog).getByRole('button', { name: '确定' }))
  await waitFor(() => expect(errorToast).toHaveBeenCalledWith('保存失败'))
  expect((within(dialog).getByRole('textbox', { name: /标签/ }) as HTMLInputElement).value).toBe('test-rules')
})

it('keeps full rule-set details and editing available across mobile and desktop layouts', async () => {
  vi.stubGlobal('innerWidth', 390)
  const tag = 'geosite-openai-and-other-remote-service-domains'
  const serverNames = ['Tokyo', '香港 · CMI', 'Los Angeles', '服务器名称较长的法兰克福备用节点']
  const ruleSet = {
    id: 1, tag, type: 'remote' as const, format: 'binary' as const,
    url: 'https://example.com/services.srs', server_ids: [1, 2, 3, 4],
  }
  vi.mocked(listSharedRuleSets).mockResolvedValue([
    ruleSet,
    { id: 2, tag: 'local-rules', type: 'local', format: 'source', path: '/etc/sing-box/rules.json' },
  ])
  vi.mocked(listServers).mockResolvedValue(serverNames.map((name, index) => ({ id: index + 1, name } as Server)))
  vi.mocked(updateSharedRuleSet).mockResolvedValue({ ruleset: ruleSet })

  render(<MemoryRouter><RuleSetsPage /></MemoryRouter>)
  const list = await screen.findByRole('list', { name: '规则集' })
  const items = within(list).getAllByRole('listitem')
  expect(within(items[0]).getByText(tag)).toBeTruthy()
  expect(within(items[0]).getByText('远程')).toBeTruthy()
  expect(within(items[0]).getByText('SRS')).toBeTruthy()
  const references = within(items[0]).getByRole('group', { name: '引用服务器' })
  for (const name of serverNames) expect(within(references).getByText(name)).toBeTruthy()
  expect(within(items[1]).getByText('本地')).toBeTruthy()
  expect(within(items[1]).getByText('JSON')).toBeTruthy()
  expect(within(items[1]).getByText('未引用')).toBeTruthy()
  expect(screen.queryByRole('table')).toBeNull()

  fireEvent.click(within(items[0]).getByRole('button', { name: `编辑规则集 ${tag}` }))
  const dialog = screen.getByRole('dialog', { name: '编辑规则集' })
  const url = within(dialog).getByRole('textbox', { name: /^URL/ }) as HTMLInputElement
  expect(url.value).toBe(ruleSet.url)
  fireEvent.change(url, { target: { value: 'https://example.com/updated.srs' } })

  act(() => {
    vi.stubGlobal('innerWidth', 1024)
    window.dispatchEvent(new Event('resize'))
  })
  expect(screen.getByRole('table', { hidden: true })).toBeTruthy()
  expect(screen.queryByRole('list', { name: '规则集', hidden: true })).toBeNull()
  expect((within(dialog).getByRole('textbox', { name: /^URL/ }) as HTMLInputElement).value).toBe('https://example.com/updated.srs')
  fireEvent.click(within(dialog).getByRole('button', { name: '确定' }))
  await waitFor(() => expect(updateSharedRuleSet).toHaveBeenCalledWith(1, expect.objectContaining({ tag, url: 'https://example.com/updated.srs', update_interval: '1440m' })))
})

it('normalizes legacy rule-set update_interval into minutes and submits preset minutes', async () => {
  vi.mocked(listSharedRuleSets).mockResolvedValue([{
    id: 5, tag: 'geosite-ads', type: 'remote', format: 'binary',
    url: 'https://example.com/ads.srs', update_interval: '12h', server_ids: [],
  }])
  vi.mocked(updateSharedRuleSet).mockResolvedValue({
    ruleset: { id: 5, tag: 'geosite-ads', type: 'remote', format: 'binary', url: 'https://example.com/ads.srs', update_interval: '60m' },
  })
  render(<MemoryRouter><RuleSetsPage /></MemoryRouter>)
  await screen.findByText('geosite-ads')
  fireEvent.click(screen.getByRole('button', { name: '编辑规则集 geosite-ads' }))
  const dialog = screen.getByRole('dialog', { name: '编辑规则集' })
  const intervalSelect = within(dialog).getByLabelText('更新间隔')
  expect(intervalSelect.textContent).toContain('720 分钟')
  await act(async () => { fireEvent.click(intervalSelect) })
  await act(async () => { fireEvent.click(screen.getByRole('option', { name: '60 分钟' })) })
  fireEvent.click(within(dialog).getByRole('button', { name: '确定' }))
  await waitFor(() => expect(updateSharedRuleSet).toHaveBeenCalledWith(5, expect.objectContaining({ update_interval: '60m' })))
})
