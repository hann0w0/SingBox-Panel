import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { THEME_STORAGE_KEY, USER_THEME_STORAGE_KEY, ThemePreferenceProvider, useThemePreference, type ThemeScope } from './themePreference'

function Picker({ scope = 'admin' }: { scope?: ThemeScope }) {
  const { themeId, setTheme } = useThemePreference(scope)
  return <><output data-testid="theme">{themeId}</output><button onClick={() => setTheme('blue')}>经典</button><button onClick={() => setTheme('green')}>青绿</button></>
}
const mount = (scope: ThemeScope = 'admin') => render(<ThemePreferenceProvider><Picker scope={scope} /></ThemePreferenceProvider>)
const current = () => screen.getByTestId('theme').textContent

beforeEach(() => {
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
    window.localStorage.setItem(THEME_STORAGE_KEY, 'green')
    const first = mount('user')
    expect(current()).toBe('warm')
    fireEvent.click(screen.getByRole('button', { name: '经典' }))
    expect(window.localStorage.getItem(USER_THEME_STORAGE_KEY)).toBe('blue')
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBe('green')
    first.unmount()
    const second = mount('user')
    expect(current()).toBe('blue')
    second.unmount()
    mount()
    expect(current()).toBe('green')
  })

  it('restricts user themes on load, writes and cross-tab updates', () => {
    window.localStorage.setItem(USER_THEME_STORAGE_KEY, 'green')
    mount('user')
    expect(current()).toBe('warm')
    fireEvent.click(screen.getByRole('button', { name: '青绿' }))
    expect(window.localStorage.getItem(USER_THEME_STORAGE_KEY)).toBe('warm')
    act(() => window.dispatchEvent(new StorageEvent('storage', { key: USER_THEME_STORAGE_KEY, newValue: 'blue' })))
    expect(current()).toBe('blue')
    act(() => window.dispatchEvent(new StorageEvent('storage', { key: THEME_STORAGE_KEY, newValue: 'green' })))
    expect(current()).toBe('blue')
    act(() => window.dispatchEvent(new StorageEvent('storage', { key: USER_THEME_STORAGE_KEY, newValue: 'green' })))
    expect(current()).toBe('warm')
    fireEvent.click(screen.getByRole('button', { name: '经典' }))
    act(() => window.dispatchEvent(new StorageEvent('storage', { key: null })))
    expect(current()).toBe('warm')
  })

  it('defaults to classic for a missing or invalid saved preference', () => {
    const first = mount()
    expect(current()).toBe('blue')
    first.unmount()
    window.localStorage.setItem(THEME_STORAGE_KEY, 'removed-theme')
    mount()
    expect(current()).toBe('blue')
  })

  it('remembers the selected palette when the app is reopened', () => {
    const first = mount()
    fireEvent.click(screen.getByRole('button', { name: '青绿' }))
    expect(current()).toBe('green')
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBe('green')
    first.unmount()
    mount()
    expect(current()).toBe('green')
  })

  it('still switches themes when browser storage rejects writes', () => {
    mount()
    vi.mocked(window.localStorage.setItem).mockImplementation(() => { throw new DOMException('Quota exceeded') })
    fireEvent.click(screen.getByRole('button', { name: '青绿' }))
    expect(current()).toBe('green')
  })

  it('renders safely when browser storage cannot be read', () => {
    vi.mocked(window.localStorage.getItem).mockImplementation(() => { throw new DOMException('Blocked') })
    mount()
    expect(current()).toBe('blue')
  })

  it('synchronizes theme changes from another tab without reacting to other preferences', () => {
    mount()
    act(() => window.dispatchEvent(new StorageEvent('storage', { key: THEME_STORAGE_KEY, newValue: 'green' })))
    expect(current()).toBe('green')
    act(() => window.dispatchEvent(new StorageEvent('storage', { key: 'another-key', newValue: 'blue' })))
    expect(current()).toBe('green')
    act(() => window.dispatchEvent(new StorageEvent('storage', { key: THEME_STORAGE_KEY, newValue: null })))
    expect(current()).toBe('blue')
  })
})
