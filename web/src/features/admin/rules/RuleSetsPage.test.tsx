import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { listServers, listSharedRuleSets, updateSharedRuleSet } from '../../../api'
import type { Server } from '../../../types'
import RuleSetsPage from './RuleSetsPage'

vi.mock('../../../api', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../../api')>(),
  listSharedRuleSets: vi.fn(),
  listServers: vi.fn(),
  updateSharedRuleSet: vi.fn(),
}))

beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('innerWidth', 1024)
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
  await waitFor(() => expect(updateSharedRuleSet).toHaveBeenCalledWith(1, expect.objectContaining({ tag, url: 'https://example.com/updated.srs' })))
})
