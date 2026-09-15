import { createContext, useContext, useEffect, useMemo, useState, useSyncExternalStore, type ReactNode } from 'react'
import { parseConsoleTheme, parseUserConsoleTheme, type ConsoleThemeId, type ConsoleThemePreference } from './theme'

export const THEME_STORAGE_KEY = 'singbox-panel-theme'
export const USER_THEME_STORAGE_KEY = 'singbox-panel-user-theme'
export type ThemeScope = 'admin' | 'user'

const SYSTEM_DARK_QUERY = '(prefers-color-scheme: dark)'

function getSystemTheme(): ConsoleThemeId {
  return typeof window !== 'undefined' && window.matchMedia?.(SYSTEM_DARK_QUERY).matches ? 'dark' : 'blue'
}

function subscribeToSystemTheme(onChange: () => void) {
  if (typeof window === 'undefined' || !window.matchMedia) return () => {}
  const media = window.matchMedia(SYSTEM_DARK_QUERY)
  media.addEventListener('change', onChange)
  return () => media.removeEventListener('change', onChange)
}

function preferenceConfig(scope: ThemeScope) {
  return scope === 'admin'
    ? { key: THEME_STORAGE_KEY, parse: parseConsoleTheme }
    : { key: USER_THEME_STORAGE_KEY, parse: parseUserConsoleTheme }
}

function readPreference(scope: ThemeScope): ConsoleThemePreference {
  const { key, parse } = preferenceConfig(scope)
  try { return parse(window.localStorage.getItem(key)) }
  catch { return parse(null) }
}

const ThemePreferenceContext = createContext<{
  adminThemeId: ConsoleThemePreference
  userThemeId: ConsoleThemePreference
  systemThemeId: ConsoleThemeId
  setTheme: (scope: ThemeScope, id: ConsoleThemePreference) => void
} | null>(null)

export function ThemePreferenceProvider({ children }: { children: ReactNode }) {
  const [adminThemeId, setAdminThemeId] = useState(() => readPreference('admin'))
  const [userThemeId, setUserThemeId] = useState(() => readPreference('user'))
  const systemThemeId = useSyncExternalStore(subscribeToSystemTheme, getSystemTheme, () => 'blue' as const)

  useEffect(() => {
    const sync = (event: StorageEvent) => {
      if (event.key === THEME_STORAGE_KEY || event.key === null) {
        setAdminThemeId(parseConsoleTheme(event.newValue))
      }
      if (event.key === USER_THEME_STORAGE_KEY || event.key === null) {
        setUserThemeId(parseUserConsoleTheme(event.newValue))
      }
    }
    window.addEventListener('storage', sync)
    return () => window.removeEventListener('storage', sync)
  }, [])

  const value = useMemo(() => ({
    adminThemeId,
    userThemeId,
    systemThemeId,
    setTheme: (scope: ThemeScope, id: ConsoleThemePreference) => {
      const { key, parse } = preferenceConfig(scope)
      const next = parse(id)
      if (scope === 'admin') setAdminThemeId(next)
      else setUserThemeId(next)
      try { window.localStorage.setItem(key, next) }
      catch { /* The current page can still switch when browser storage is unavailable. */ }
    },
  }), [adminThemeId, userThemeId, systemThemeId])

  return <ThemePreferenceContext.Provider value={value}>{children}</ThemePreferenceContext.Provider>
}

export function useThemePreference(scope: ThemeScope = 'admin') {
  const value = useContext(ThemePreferenceContext)
  if (!value) throw new Error('ThemePreferenceProvider is missing')
  const themeId = scope === 'admin' ? value.adminThemeId : value.userThemeId
  return {
    themeId,
    resolvedThemeId: themeId === 'auto' ? value.systemThemeId : themeId,
    setTheme: (id: ConsoleThemePreference) => value.setTheme(scope, id),
  }
}
