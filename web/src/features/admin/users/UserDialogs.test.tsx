import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createUser, updateUser } from '../../../api'
import type { User } from '../../../types'
import { UserAccountDialog } from './UserDialogs'

vi.mock('../../../api', async (original) => ({ ...await original<typeof import('../../../api')>(), updateUser: vi.fn(), createUser: vi.fn() }))
const user = { id: 2, email: 'alice', forwarding_enabled: true, role: 'user' } as User
beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  vi.mocked(updateUser).mockResolvedValue({ ...user, forwarding_enabled: false })
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

it('warns before disabling forwards and keeps the draft when confirmation is cancelled', async () => {
  const onSaved = vi.fn()
  const onClose = vi.fn()
  render(<UserAccountDialog user={user} onSaved={onSaved} onClose={onClose} />)
  fireEvent.change(screen.getByRole('textbox', { name: /用户名/ }), { target: { value: 'alice-renamed' } })
  fireEvent.click(screen.getByRole('switch', { name: '允许创建转发' }))
  fireEvent.click(screen.getByRole('button', { name: '确定' }))
  const confirmation = await screen.findByRole('dialog', { name: '关闭转发权限？' })
  expect(within(confirmation).getByRole('alert').textContent).toContain('所有转发配置')
  expect(confirmation.textContent).toContain('订阅节点')
  expect(confirmation.textContent).toContain('路由规则')
  expect(confirmation.textContent).toContain('重新开启权限不会恢复')
  expect(updateUser).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: '继续编辑' }))
  expect((screen.getByRole('textbox', { name: /用户名/ }) as HTMLInputElement).value).toBe('alice-renamed')
  expect(screen.getByRole('switch').getAttribute('aria-checked')).toBe('false')
  expect(onClose).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: '确定' }))
  fireEvent.click(await screen.findByRole('button', { name: '确认关闭并保存' }))
  await waitFor(() => expect(updateUser).toHaveBeenCalledTimes(1))
  expect(updateUser).toHaveBeenCalledWith(2, expect.objectContaining({ email: 'alice-renamed', forwarding_enabled: false }))
  await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  expect(onSaved).toHaveBeenCalledTimes(1)
})

it('preserves the confirmation after a failed save and guards duplicate submits', async () => {
  let reject!: (error: Error) => void
  vi.mocked(updateUser).mockReturnValueOnce(new Promise((_, fail) => { reject = fail }))
  render(<UserAccountDialog user={user} onSaved={vi.fn()} onClose={vi.fn()} />)
  fireEvent.click(screen.getByRole('switch'))
  fireEvent.click(screen.getByRole('button', { name: '确定' }))
  const confirm = await screen.findByRole('button', { name: '确认关闭并保存' })
  fireEvent.click(confirm)
  fireEvent.click(confirm)
  await waitFor(() => expect(updateUser).toHaveBeenCalledTimes(1))
  reject(new Error('保存失败'))
  await screen.findByText('保存失败')
  expect(screen.getByRole('dialog', { name: '关闭转发权限？' })).toBeTruthy()
  fireEvent.click(confirm)
  await waitFor(() => expect(updateUser).toHaveBeenCalledTimes(2))
})

it.each([false, true])('saves without destructive confirmation when forwarding is not disabled (%s)', async (enabled) => {
  render(<UserAccountDialog user={{ ...user, forwarding_enabled: enabled }} onSaved={vi.fn()} onClose={vi.fn()} />)
  fireEvent.click(screen.getByRole('button', { name: '确定' }))
  await waitFor(() => expect(updateUser).toHaveBeenCalledTimes(1))
  expect(screen.queryByRole('button', { name: '确认关闭并保存' })).toBeNull()
})
