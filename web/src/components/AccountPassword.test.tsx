import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const mockHttp = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  delete: vi.fn(),
  interceptors: {
    request: { use: vi.fn() },
    response: { use: vi.fn() },
  },
}))

vi.mock('axios', () => ({
  default: {
    create: () => mockHttp,
    isAxiosError: () => false,
    isCancel: () => false,
  },
}))

vi.mock('../lib/themePreference', () => ({
  useThemePreference: () => ({ themeId: 'default', resolvedThemeId: 'blue', setTheme: vi.fn() }),
}))

import AppLayout from './Layout'
import { changeMyPassword, updateUser } from '../lib/api'
import { useAuth } from '../lib/store'
import type { User } from '../lib/types'
import { ToastHost } from './kit/feedback'
import { Modal } from './kit/dialogs'

const adminUser: User = {
  id: 1,
  email: 'admin',
  role: 'admin',
  enabled: true,
  forwarding_enabled: true,
  server_ids: [],
  inbound_ids: [],
  expire_at: null,
  created_at: '',
  updated_at: '',
}

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
  window.scrollTo = vi.fn()
  useAuth.setState({ token: 'old-token', user: adminUser })
})

afterEach(() => {
  cleanup()
  Modal.destroyAll()
  useAuth.setState({ token: null, user: null })
  vi.unstubAllGlobals()
})

describe('self-password token rotation', () => {
  it('rotates useAuth token only when updateUser returns a token for self', async () => {
    mockHttp.put
      .mockResolvedValueOnce({ data: { user: adminUser, token: 'rotated-self-token' } })
      .mockResolvedValueOnce({ data: { user: { ...adminUser, id: 2, email: 'bob' } } })

    await updateUser(1, { password: 'new-self-password' })
    expect(useAuth.getState().token).toBe('rotated-self-token')

    await updateUser(2, { password: 'other-user-password' })
    expect(useAuth.getState().token).toBe('rotated-self-token')
    expect(mockHttp.put).toHaveBeenCalledTimes(2)
  })

  it('updates auth store from AccountBar change password modal via changeMyPassword', async () => {
    mockHttp.post.mockResolvedValueOnce({ data: { ok: true, token: 'user-pw-rotated-token' } })

    render(
      <MemoryRouter initialEntries={['/admin/servers']}>
        <ToastHost />
        <AppLayout />
      </MemoryRouter>,
    )

    fireEvent.click(screen.getByRole('button', { name: '修改密码' }))
    fireEvent.change(screen.getByLabelText(/^当前密码/), { target: { value: 'old-pw' } })
    fireEvent.change(screen.getByLabelText(/^新密码/), { target: { value: 'new-pw' } })
    fireEvent.change(screen.getByLabelText(/^确认新密码/), { target: { value: 'new-pw' } })
    fireEvent.click(screen.getByRole('button', { name: '保存密码' }))

    await waitFor(() => expect(mockHttp.post).toHaveBeenCalledWith('/api/user/change-password', {
      old_password: 'old-pw',
      new_password: 'new-pw',
    }))
    expect(useAuth.getState().token).toBe('user-pw-rotated-token')

    mockHttp.post.mockResolvedValueOnce({ data: { ok: true, token: 'direct-call-token' } })
    await changeMyPassword('new-pw', 'newer-pw')
    expect(useAuth.getState().token).toBe('direct-call-token')
  })
})
