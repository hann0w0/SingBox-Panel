import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { THEME_STORAGE_KEY, USER_THEME_STORAGE_KEY, ThemePreferenceProvider, useThemePreference, type ThemeScope } from './themePreference'

function Picker({ scope = 'admin' }: { scope?: ThemeScope }) {
  const { themeId, resolvedThemeId, setTheme } = useThemePreference(scope)
  return <>
    <output data-testid="theme">{themeId}</output>
    <output data-testid="resolved-theme">{resolvedThemeId}</output>
    <button onClick={() => setTheme('auto')}>自动</button>
    <button onClick={() => setTheme('blue')}>日间</button>
    <button onClick={() => setTheme('dark')}>夜间</button>
  </>
}
const mount = (scope: ThemeScope = 'admin') => render(<ThemePreferenceProvider><Picker scope={scope} /></ThemePreferenceProvider>)
const current = () => screen.getByTestId('theme').textContent
const resolved = () => screen.getByTestId('resolved-theme').textContent
let systemDark = false
const systemListeners = new Set<() => void>()
const changeSystemTheme = (dark: boolean) => act(() => {
  systemDark = dark
  systemListeners.forEach((listener) => listener())
})

beforeEach(() => {
  systemDark = false
  systemListeners.clear()
  vi.stubGlobal('matchMedia', () => ({
    matches: systemDark,
    addEventListener: (_event: string, listener: () => void) => { systemListeners.add(listener) },
    removeEventListener: (_event: string, listener: () => void) => { systemListeners.delete(listener) },
  }))
  const values = new Map<string, string>()
  vi.stubGlobal('localStorage', {
    getItem: vi.fn((key: string) => values.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => { values.set(key, value) }),
    removeItem: vi.fn((key: string) => { values.delete(key) }),
  })
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('theme preference', () => {
  it('remembers the user choice separately from the admin preference', () => {
    window.localStorage.setItem(THEME_STORAGE_KEY, 'blue')
    const first = mount('user')
    expect(current()).toBe('blue')
    fireEvent.click(screen.getByRole('button', { name: '夜间' }))
    expect(window.localStorage.getItem(USER_THEME_STORAGE_KEY)).toBe('dark')
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBe('blue')
    first.unmount()
    const second = mount('user')
    expect(current()).toBe('dark')
    second.unmount()
    mount()
    expect(current()).toBe('blue')
  })

  it.each(['admin', 'user'] as const)('supports theme cross-tab updates for %s', (scope) => {
    const key = scope === 'admin' ? THEME_STORAGE_KEY : USER_THEME_STORAGE_KEY
    const otherKey = scope === 'admin' ? USER_THEME_STORAGE_KEY : THEME_STORAGE_KEY
    mount(scope)
    act(() => window.dispatchEvent(new StorageEvent('storage', { key, newValue: 'dark' })))
    expect(current()).toBe('dark')
    act(() => window.dispatchEvent(new StorageEvent('storage', { key: otherKey, newValue: 'blue' })))
    expect(current()).toBe('dark')
    act(() => window.dispatchEvent(new StorageEvent('storage', { key, newValue: 'auto' })))
    expect(current()).toBe('auto')
    expect(resolved()).toBe('blue')
    changeSystemTheme(true)
    expect(resolved()).toBe('dark')
    act(() => window.dispatchEvent(new StorageEvent('storage', { key, newValue: 'warm' })))
    expect(current()).toBe('blue')
    fireEvent.click(screen.getByRole('button', { name: '夜间' }))
    act(() => window.dispatchEvent(new StorageEvent('storage', { key: null })))
    expect(current()).toBe('blue')
  })

  it.each(['admin', 'user'] as const)('remembers auto for %s and follows live system changes', (scope) => {
    const key = scope === 'admin' ? THEME_STORAGE_KEY : USER_THEME_STORAGE_KEY
    systemDark = true
    const view = mount(scope)
    fireEvent.click(screen.getByRole('button', { name: '自动' }))
    expect(current()).toBe('auto')
    expect(resolved()).toBe('dark')
    expect(window.localStorage.getItem(key)).toBe('auto')
    changeSystemTheme(false)
    expect(current()).toBe('auto')
    expect(resolved()).toBe('blue')
    expect(window.localStorage.getItem(key)).toBe('auto')
    view.unmount()
    expect(systemListeners.size).toBe(0)
    systemDark = true
    mount(scope)
    expect(current()).toBe('auto')
    expect(resolved()).toBe('dark')
  })

  it.each(['admin', 'user'] as const)('keeps manual %s choices until auto is selected', (scope) => {
    mount(scope)
    changeSystemTheme(true)
    expect(resolved()).toBe('blue')
    fireEvent.click(screen.getByRole('button', { name: '夜间' }))
    changeSystemTheme(false)
    expect(resolved()).toBe('dark')
    fireEvent.click(screen.getByRole('button', { name: '自动' }))
    expect(resolved()).toBe('blue')
    changeSystemTheme(true)
    expect(resolved()).toBe('dark')
  })

  it('falls back to day in auto mode when media queries are unavailable', () => {
    vi.stubGlobal('matchMedia', undefined)
    mount()
    fireEvent.click(screen.getByRole('button', { name: '自动' }))
    expect(current()).toBe('auto')
    expect(resolved()).toBe('blue')
  })

  it.each([
    ['admin', 'warm'], ['admin', 'green'], ['user', 'warm'], ['user', 'green'],
  ] as const)('falls back to day for the removed %s preference %s', (scope, previous) => {
    window.localStorage.setItem(scope === 'admin' ? THEME_STORAGE_KEY : USER_THEME_STORAGE_KEY, previous)
    mount(scope)
    expect(current()).toBe('blue')
  })

  it('defaults to day for a missing or invalid saved preference', () => {
    const first = mount()
    expect(current()).toBe('blue')
    first.unmount()
    window.localStorage.setItem(THEME_STORAGE_KEY, 'removed-theme')
    mount()
    expect(current()).toBe('blue')
  })

  it('remembers the selected palette when the app is reopened', () => {
    const first = mount()
    fireEvent.click(screen.getByRole('button', { name: '夜间' }))
    expect(current()).toBe('dark')
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBe('dark')
    first.unmount()
    mount()
    expect(current()).toBe('dark')
  })

  it('still switches themes when browser storage rejects writes', () => {
    mount()
    vi.mocked(window.localStorage.setItem).mockImplementation(() => { throw new DOMException('Quota exceeded') })
    fireEvent.click(screen.getByRole('button', { name: '夜间' }))
    expect(current()).toBe('dark')
  })

  it('renders safely when browser storage cannot be read', () => {
    vi.mocked(window.localStorage.getItem).mockImplementation(() => { throw new DOMException('Blocked') })
    mount()
    expect(current()).toBe('blue')
  })

  it('synchronizes theme changes from another tab without reacting to other preferences', () => {
    mount()
    act(() => window.dispatchEvent(new StorageEvent('storage', { key: THEME_STORAGE_KEY, newValue: 'dark' })))
    expect(current()).toBe('dark')
    act(() => window.dispatchEvent(new StorageEvent('storage', { key: 'another-key', newValue: 'blue' })))
    expect(current()).toBe('dark')
    act(() => window.dispatchEvent(new StorageEvent('storage', { key: THEME_STORAGE_KEY, newValue: null })))
    expect(current()).toBe('blue')
  })
})
