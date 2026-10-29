import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Dashboard from './Dashboard'
import { getMe, getUserNodes } from '../../api'
import type { UserNode } from '../../api'
import type { User } from '../../types'
import { useAuth } from '../../store'
import { copyToClipboard } from '../../util'

vi.mock('../../api', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../api')>(),
  getMe: vi.fn(), getUserNodes: vi.fn(),
}))
vi.mock('../../util', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../util')>(), copyToClipboard: vi.fn(),
}))

const user = { id: 2, email: 'alice', role: 'user', enabled: true, server_ids: [1], inbound_ids: [1] } as User
const node: UserNode = {
  name: 'Tokyo', type: 'vless', server: 'tokyo.example', port: 443, region: 'JP',
  link: 'vless://old-uuid@tokyo.example:443', params: { UUID: 'old-uuid' },
}

beforeEach(() => {
  vi.clearAllMocks()
  useAuth.setState({ token: 'test-token', user })
  vi.mocked(getMe).mockResolvedValue({ user, subscription_url: 'https://panel.example/sub/test' })
  vi.mocked(getUserNodes).mockResolvedValue([node])
  vi.mocked(copyToClipboard).mockResolvedValue(undefined)
})
afterEach(() => {
  cleanup()
  useAuth.setState({ token: null, user: null })
  vi.unstubAllGlobals()
})

describe('user subscription dashboard', () => {
  it.each([false, true])('places reset below the subscription links without a page title or password action (mobile: %s)', async (mobile) => {
    vi.stubGlobal('innerWidth', mobile ? 390 : 1280)
    render(<Dashboard />)
    const heading = await screen.findByRole('heading', { name: '订阅链接', level: 2 })
    const section = heading.closest('section')!
    expect(screen.queryByRole('heading', { level: 1 })).toBeNull()
    expect(document.querySelector('.ui-page-meta')).toBeNull()
    const reset = within(section).getByRole('button', { name: '重置链接' })
    expect(reset.closest('.sub-actions')?.previousElementSibling?.classList.contains('sub-clients')).toBe(true)
    expect(screen.queryByText(user.email)).toBeNull()
    expect(screen.queryByRole('button', { name: '修改密码' })).toBeNull()
    expect(document.querySelector('input[type="password"]')).toBeNull()
    await screen.findByText('Tokyo')
    for (const [label, target] of [['Surge', 'surge'], ['ClashMeta', 'clash'], ['Shadowrocket', 'shadowrocket']]) {
      fireEvent.click(screen.getByRole('button', { name: `复制 ${label} 订阅链接` }))
      await waitFor(() => expect(copyToClipboard).toHaveBeenLastCalledWith(`https://panel.example/sub/test?target=${target}`))
    }
  })
})
