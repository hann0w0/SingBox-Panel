import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { OutboundImport } from './features/admin/servers/OutboundImport'
import App from './App'
import { RouteErrorBoundary } from './components/RouteErrorBoundary'
import { AUTH_STORAGE_KEY, useAuth } from './lib/store'
import { getMe, listOutboundSources } from './lib/api'
import type { User } from './lib/types'
import { ThemePreferenceProvider } from './lib/themePreference'
import { TooltipProvider } from './components/ui/tooltip'

vi.mock('./lib/api', async (importOriginal) => ({
  ...await importOriginal<typeof import('./lib/api')>(),
  listOutboundSources: vi.fn(),
  listSharedRuleSets: vi.fn(async () => []),
  listServers: vi.fn(async () => []),
  getMe: vi.fn(),
}))

const sampleUser: User = {
  id: 7,
  email: 'admin',
  role: 'admin',
  enabled: true,
  forwarding_enabled: false,
  server_ids: [],
  inbound_ids: [],
  expire_at: null,
  created_at: '',
  updated_at: '',
}

const origLocation = window.location

function createMemoryStorage(): Storage {
  const data = new Map<string, string>()
  return {
    get length() { return data.size },
    clear() { data.clear() },
    getItem(key: string) { return data.get(key) ?? null },
    key(index: number) { return Array.from(data.keys())[index] ?? null },
    removeItem(key: string) { data.delete(key) },
    setItem(key: string, value: string) { data.set(key, String(value)) },
  }
}

beforeEach(() => {
  vi.resetAllMocks()
  const memLocal = createMemoryStorage()
  const memSession = createMemoryStorage()
  vi.stubGlobal('localStorage', memLocal)
  vi.stubGlobal('sessionStorage', memSession)
  Object.defineProperty(window, 'localStorage', { configurable: true, value: memLocal })
  Object.defineProperty(window, 'sessionStorage', { configurable: true, value: memSession })
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
  useAuth.setState({ token: null, user: null, needsRevalidation: false })
})

afterEach(() => {
  cleanup()
  Object.defineProperty(window, 'location', { configurable: true, value: origLocation })
  useAuth.setState({ token: null, user: null, needsRevalidation: false })
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('Node 7 regressions', () => {
  it('renders retry button when OutboundImport fails to load sources and reloads on click', async () => {
    vi.mocked(listOutboundSources)
      .mockRejectedValueOnce(new Error('加载出站来源失败'))
      .mockResolvedValueOnce([
        { kind: 'inbound', id: 11, name: 'HK - vless', group: 'HK', type: 'vless' },
      ])

    render(<OutboundImport serverId={1} mode="panel" onClose={vi.fn()} onSaved={vi.fn()} />)
    expect(await screen.findByText('加载出站来源失败')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    expect(await screen.findByText('HK')).toBeTruthy()
    expect(listOutboundSources).toHaveBeenCalledTimes(2)
  })

  it('syncs cross-tab storage logout and revalidates persisted sessions on reload', async () => {
    vi.mocked(getMe).mockResolvedValue({ user: { ...sampleUser, forwarding_enabled: true }, subscription_url: '' })
    window.localStorage.setItem(AUTH_STORAGE_KEY, JSON.stringify({ state: { token: 'persisted-jwt', user: sampleUser }, version: 0 }))
    await useAuth.persist.rehydrate()
    expect(useAuth.getState().needsRevalidation).toBe(true)

    render(
      <MemoryRouter initialEntries={['/admin/rules']}>
        <ThemePreferenceProvider>
          <TooltipProvider>
            <App />
          </TooltipProvider>
        </ThemePreferenceProvider>
      </MemoryRouter>,
    )

    await waitFor(() => expect(getMe).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(useAuth.getState().user?.forwarding_enabled).toBe(true))
    expect(useAuth.getState().needsRevalidation).toBe(false)

    // Cross-tab logout clears store immediately.
    window.dispatchEvent(new StorageEvent('storage', { key: AUTH_STORAGE_KEY, newValue: null }))
    await waitFor(() => expect(useAuth.getState().token).toBeNull())
    expect(useAuth.getState().user).toBeNull()
  })

  it('shows a logout escape hatch when initial user hydration fails without user state', async () => {
    vi.mocked(getMe).mockRejectedValueOnce(new Error('后端暂不可用'))
    useAuth.setState({ token: 'orphan-jwt', user: null, needsRevalidation: false })

    render(
      <MemoryRouter initialEntries={['/admin/rules']}>
        <ThemePreferenceProvider>
          <TooltipProvider>
            <App />
          </TooltipProvider>
        </ThemePreferenceProvider>
      </MemoryRouter>,
    )

    expect(await screen.findByText(/加载用户信息失败：后端暂不可用/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '退出登录' }))
    await waitFor(() => expect(useAuth.getState().token).toBeNull())
  })

  it('auto-reloads once on chunk load failures within cooldown in RouteErrorBoundary', () => {
    const reload = vi.fn()
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...origLocation, pathname: '/admin/servers', reload, assign: vi.fn() },
    })
    vi.spyOn(console, 'error').mockImplementation(() => {})

    function Bomb(): JSX.Element {
      throw new Error('Failed to fetch dynamically imported module: /assets/ServerDetail-abc.js')
    }

    const { unmount } = render(
      <RouteErrorBoundary resetKey="/admin/servers">
        <Bomb />
      </RouteErrorBoundary>,
    )
    expect(reload).toHaveBeenCalledTimes(1)
    unmount()

    // Immediate second failure inside cooldown must not reload again automatically.
    render(
      <RouteErrorBoundary resetKey="/admin/servers">
        <Bomb />
      </RouteErrorBoundary>,
    )
    expect(reload).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    expect(reload).toHaveBeenCalledTimes(2)
  })
})
