import { cleanup, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { listCustomNodes, listServers, listUsers } from '../../../api'
import type { CustomNode } from '../../../api'
import type { Server, User } from '../../../types'
import Users from './Users'

vi.mock('../../../api', async (original) => ({ ...await original<typeof import('../../../api')>(), listUsers: vi.fn(), listServers: vi.fn(), listCustomNodes: vi.fn() }))
beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('innerWidth', 1280)
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

it('shows assigned servers and visible external groups with exclusions, deduplication, and count ordering', async () => {
  vi.mocked(listUsers).mockResolvedValue([
    { id: 1, email: 'alice', server_ids: [10, 99] },
    { id: 2, email: 'bob', server_ids: [] },
    { id: 3, email: 'carol', server_ids: [] },
  ] as unknown as User[])
  vi.mocked(listServers).mockResolvedValue([{ id: 10, name: 'Tokyo' }] as Server[])
  vi.mocked(listCustomNodes).mockResolvedValue([
    { id: 1, all_users: true, excluded_user_ids: [1, 3], group: 'Shared' },
    { id: 2, all_users: false, user_ids: [1, 1], group: 'Personal' },
    { id: 3, all_users: false, user_ids: [1, 2], group: 'Popular' },
    { id: 4, all_users: false, user_ids: [1, 2], group: 'Popular' },
    { id: 5, all_users: false, user_ids: [2], group: '' },
  ] as unknown as CustomNode[])
  render(<MemoryRouter><Users /></MemoryRouter>)
  const aliceRow = (await screen.findByRole('link', { name: '管理 alice' })).closest('tr')!
  await within(aliceRow).findByText('Tokyo')
  const aliceChips = aliceRow.querySelector('.um-chips')!
  expect(aliceChips.getAttribute('title')).toBe('Tokyo、#99、Popular、Personal')
  expect(within(aliceRow).queryByText('Shared')).toBeNull()
  expect(within(aliceRow).getByText('+1')).toBeTruthy()
  const bobRow = screen.getByRole('link', { name: '管理 bob' }).closest('tr')!
  expect(bobRow.querySelector('.um-chips')?.getAttribute('title')).toBe('Popular、Shared、未分组')
  expect(within(bobRow).queryByText('Personal')).toBeNull()
  const carolRow = screen.getByRole('link', { name: '管理 carol' }).closest('tr')!
  expect(within(carolRow).getByText('未分配')).toBeTruthy()
})
