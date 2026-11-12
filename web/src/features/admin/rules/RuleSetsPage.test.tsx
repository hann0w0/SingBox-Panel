import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { listServers, listSharedRuleSets } from '../../../api'
import RuleSetsPage from './RuleSetsPage'

vi.mock('../../../api', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../../api')>(),
  listSharedRuleSets: vi.fn(),
  listServers: vi.fn(),
}))

beforeEach(() => {
  vi.resetAllMocks()
  vi.mocked(listSharedRuleSets).mockResolvedValue([{
    id: 1, tag: 'geosite-cn', type: 'remote', format: 'binary',
    url: 'https://example.com/cn.srs', server_ids: [2],
  }])
  vi.mocked(listServers).mockResolvedValue([{ id: 2, name: 'Tokyo' } as Awaited<ReturnType<typeof listServers>>[number]])
})

afterEach(cleanup)

it('shows referenced servers without a manual assignment action', async () => {
  render(<MemoryRouter><RuleSetsPage /></MemoryRouter>)
  expect(await screen.findByText('geosite-cn')).toBeTruthy()
  expect(screen.getByText('Tokyo')).toBeTruthy()
  expect(screen.queryByRole('button', { name: '分配' })).toBeNull()
  expect(screen.getByRole('button', { name: '编辑规则集 geosite-cn' })).toBeTruthy()
})
