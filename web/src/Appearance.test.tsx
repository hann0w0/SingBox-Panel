import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { theme } from 'antd'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Appearance from './Appearance'
import { THEME_STORAGE_KEY, USER_THEME_STORAGE_KEY, ThemePreferenceProvider, useThemePreference } from './themePreference'

const auth = vi.hoisted(() => ({ role: 'admin' }))
vi.mock('./store', () => ({ useAuth: (select: (state: { user: { role: string } }) => unknown) => select({ user: { role: auth.role } }) }))
vi.mock('./App', () => ({
  default: function ThemeProbe() {
    const { token } = theme.useToken()
    const { setTheme } = useThemePreference(auth.role === 'admin' ? 'admin' : 'user')
    return <><output data-testid="primary">{token.colorPrimary}</output><output data-testid="selection">{token.controlItemBgActive}</output><button onClick={() => setTheme('green')}>选择青绿</button><button onClick={() => setTheme('blue')}>选择经典</button></>
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
  it('defaults the admin to classic white and applies theme changes to components and custom CSS', () => {
    vi.stubGlobal('localStorage', { getItem: () => null, setItem: vi.fn() })
    render(page('/admin/overview'))
    expect(screen.getByTestId('primary').textContent).toBe('#3a5bff')
    expect(document.documentElement.dataset.consoleTheme).toBe('blue')
    expect(document.documentElement.style.getPropertyValue('--console-layout')).toBe('#f5f5f5')
    expect(document.documentElement.style.getPropertyValue('--console-surface')).toBe('#fafafa')
    expect(document.documentElement.style.getPropertyValue('--console-chrome')).toBe('#ffffff')
    fireEvent.click(screen.getByRole('button', { name: '选择青绿' }))
    expect(screen.getByTestId('primary').textContent).toBe('#247668')
    expect(document.documentElement.dataset.consoleTheme).toBe('green')
    expect(screen.getByTestId('selection').textContent).toBe('#e3f0e9')
  })

  it('lets a user switch to classic independently of the admin and rejects green', () => {
    const view = render(page('/dashboard'))
    expect(screen.getByTestId('primary').textContent).toBe('#3a5bff')
    auth.role = 'user'
    view.rerender(page('/dashboard'))
    expect(screen.getByTestId('primary').textContent).toBe('#353432')
    expect(document.documentElement.dataset.consoleTheme).toBe('warm')
    expect(screen.getByTestId('selection').textContent).toBe('#eeece6')
    fireEvent.click(screen.getByRole('button', { name: '选择青绿' }))
    expect(screen.getByTestId('primary').textContent).toBe('#353432')
    expect(document.documentElement.dataset.consoleTheme).toBe('warm')
    fireEvent.click(screen.getByRole('button', { name: '选择经典' }))
    expect(screen.getByTestId('primary').textContent).toBe('#3a5bff')
    expect(document.documentElement.dataset.consoleTheme).toBe('blue')
    expect(window.localStorage.setItem).toHaveBeenCalledWith(USER_THEME_STORAGE_KEY, 'blue')
    auth.role = 'admin'
    view.rerender(page('/dashboard'))
    fireEvent.click(screen.getByRole('button', { name: '选择青绿' }))
    expect(document.documentElement.dataset.consoleTheme).toBe('green')
    auth.role = 'user'
    view.rerender(page('/dashboard'))
    expect(document.documentElement.dataset.consoleTheme).toBe('blue')
  })

  it('keeps the login theme independent of the saved console palette', () => {
    render(page('/login'))
    fireEvent.click(screen.getByRole('button', { name: '选择青绿' }))
    expect(screen.getByTestId('primary').textContent).toBe('#3a5bff')
    expect(document.documentElement.dataset.consoleTheme).toBeUndefined()
    expect(document.documentElement.style.getPropertyValue('--console-primary')).toBe('')
  })
})
