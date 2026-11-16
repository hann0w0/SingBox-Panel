import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { getConsolePalette } from '../lib/theme'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Appearance from './Appearance'
import { THEME_STORAGE_KEY, USER_THEME_STORAGE_KEY, ThemePreferenceProvider, useThemePreference } from '../lib/themePreference'

const auth = vi.hoisted(() => ({ role: 'admin' }))
vi.mock('../lib/store', () => ({ useAuth: (select: (state: { user: { role: string } }) => unknown) => select({ user: { role: auth.role } }) }))
vi.mock('../App', () => ({
  default: function ThemeProbe() {
    const { setTheme, resolvedThemeId } = useThemePreference(auth.role === 'admin' ? 'admin' : 'user')
    const { colors: token } = getConsolePalette(useLocation().pathname === '/login' ? 'blue' : resolvedThemeId)
    return <>
      <output data-testid="primary">{token.primary}</output>
      <output data-testid="selection">{token.selected}</output>
      <output data-testid="container">{token.container}</output>
      <output data-testid="popup">{token.elevated}</output>
      <output data-testid="error-background">{token.error}</output>
      <button onClick={() => setTheme('auto')}>选择自动</button>
      <button onClick={() => setTheme('dark')}>选择夜间</button>
      <button onClick={() => setTheme('blue')}>选择日间</button>
    </>
  },
}))

beforeEach(() => {
  auth.role = 'admin'
  vi.stubGlobal('localStorage', { getItem: (key: string) => key === THEME_STORAGE_KEY ? 'blue' : null, setItem: vi.fn() })
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

const page = (path: string) => <MemoryRouter initialEntries={[path]}><ThemePreferenceProvider><Appearance /></ThemePreferenceProvider></MemoryRouter>

describe('theme scope', () => {
  it('updates component and CSS colors live when the system changes in auto mode', () => {
    let dark = true
    const media = new EventTarget()
    // matchMedia returns a live MediaQueryList; keep matches current for every snapshot.
    Object.defineProperty(media, 'matches', { get: () => dark })
    vi.stubGlobal('matchMedia', (query: string) => query === '(prefers-color-scheme: dark)'
      ? media : { matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
    vi.stubGlobal('localStorage', { getItem: () => 'auto', setItem: vi.fn() })
    render(page('/admin/overview'))
    expect(document.documentElement.dataset.consoleTheme).toBe('dark')
    expect(screen.getByTestId('popup').textContent).toBe('#2a2a2a')
    act(() => { dark = false; media.dispatchEvent(new Event('change')) })
    expect(document.documentElement.dataset.consoleTheme).toBe('blue')
    expect(document.documentElement.style.getPropertyValue('--console-container')).toBe('#ffffff')
    expect(screen.getByTestId('popup').textContent).toBe('#ffffff')
    act(() => { dark = true; media.dispatchEvent(new Event('change')) })
    expect(document.documentElement.dataset.consoleTheme).toBe('dark')
    expect(screen.getByTestId('container').textContent).toBe('#1e1e1e')
    expect(window.localStorage.setItem).not.toHaveBeenCalled()
  })

  it('switches components, popup surfaces and custom CSS between day and night', () => {
    render(page('/admin/overview'))
    expect(screen.getByTestId('primary').textContent).toBe('#111111')
    expect(document.documentElement.dataset.consoleTheme).toBe('blue')
    expect(document.documentElement.style.getPropertyValue('--console-layout')).toBe('#ffffff')
    expect(screen.getByTestId('container').textContent).toBe('#ffffff')
    const dayErrorBackground = screen.getByTestId('error-background').textContent

    fireEvent.click(screen.getByRole('button', { name: '选择夜间' }))
    expect(screen.getByTestId('primary').textContent).toBe('#ececec')
    expect(document.documentElement.dataset.consoleTheme).toBe('dark')
    expect(document.documentElement.style.getPropertyValue('--console-layout')).toBe('#1e1e1e')
    expect(document.documentElement.style.getPropertyValue('--console-container')).toBe('#1e1e1e')
    expect(screen.getByTestId('container').textContent).toBe('#1e1e1e')
    expect(screen.getByTestId('popup').textContent).toBe('#2a2a2a')
    expect(screen.getByTestId('selection').textContent).toBe('#333333')
    // Semantic colors come from the same palette as the document CSS variables.
    expect(screen.getByTestId('error-background').textContent).not.toBe(dayErrorBackground)

    fireEvent.click(screen.getByRole('button', { name: '选择日间' }))
    expect(screen.getByTestId('container').textContent).toBe('#ffffff')
    expect(screen.getByTestId('popup').textContent).toBe('#ffffff')
    expect(screen.getByTestId('error-background').textContent).toBe(dayErrorBackground)
    expect(document.documentElement.dataset.consoleTheme).toBe('blue')
  })

  it('lets users choose night independently of the admin preference', () => {
    const view = render(page('/dashboard'))
    auth.role = 'user'
    view.rerender(page('/dashboard'))
    expect(document.documentElement.dataset.consoleTheme).toBe('blue')
    fireEvent.click(screen.getByRole('button', { name: '选择夜间' }))
    expect(document.documentElement.dataset.consoleTheme).toBe('dark')
    expect(window.localStorage.setItem).toHaveBeenCalledWith(USER_THEME_STORAGE_KEY, 'dark')
    auth.role = 'admin'
    view.rerender(page('/dashboard'))
    expect(document.documentElement.dataset.consoleTheme).toBe('blue')
    auth.role = 'user'
    view.rerender(page('/dashboard'))
    expect(document.documentElement.dataset.consoleTheme).toBe('dark')
  })

  it('keeps login independent and removes console colors when leaving the console', () => {
    const view = render(page('/admin/overview'))
    fireEvent.click(screen.getByRole('button', { name: '选择夜间' }))
    view.unmount()
    vi.stubGlobal('localStorage', { getItem: () => 'dark', setItem: vi.fn() })
    render(page('/login'))
    expect(screen.getByTestId('primary').textContent).toBe('#111111')
    expect(screen.getByTestId('container').textContent).toBe('#ffffff')
    expect(document.documentElement.dataset.consoleTheme).toBe('blue')
    expect(document.documentElement.style.getPropertyValue('--console-primary')).toBe('#111111')
    expect(document.documentElement.style.getPropertyValue('--console-container')).toBe('#ffffff')
  })
})
