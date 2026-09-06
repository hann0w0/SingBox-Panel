import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { parseConsoleTheme, parseUserConsoleTheme, type ConsoleThemeId } from './theme'

export const THEME_STORAGE_KEY = 'singbox-panel-theme'
export const USER_THEME_STORAGE_KEY = 'singbox-panel-user-theme'
export type ThemeScope = 'admin' | 'user'

function preferenceConfig(scope: ThemeScope) {
  return scope === 'admin'
    ? { key: THEME_STORAGE_KEY, parse: parseConsoleTheme }
    : { key: USER_THEME_STORAGE_KEY, parse: parseUserConsoleTheme }
}

function readPreference(scope: ThemeScope): ConsoleThemeId {
  const { key, parse } = preferenceConfig(scope)
  try { return parse(window.localStorage.getItem(key)) }
  catch { return parse(null) }
}

const ThemePreferenceContext = createContext<{
  adminThemeId: ConsoleThemeId
  userThemeId: ConsoleThemeId
  setTheme: (scope: ThemeScope, id: ConsoleThemeId) => void
} | null>(null)

export function ThemePreferenceProvider({ children }: { children: ReactNode }) {
  const [adminThemeId, setAdminThemeId] = useState(() => readPreference('admin'))
  const [userThemeId, setUserThemeId] = useState(() => readPreference('user'))

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
    setTheme: (scope: ThemeScope, id: ConsoleThemeId) => {
      const { key, parse } = preferenceConfig(scope)
      const next = parse(id)
      if (scope === 'admin') setAdminThemeId(next)
      else setUserThemeId(next)
      try { window.localStorage.setItem(key, next) }
      catch { /* The current page can still switch when browser storage is unavailable. */ }
    },
  }), [adminThemeId, userThemeId])

  return <ThemePreferenceContext.Provider value={value}>{children}</ThemePreferenceContext.Provider>
}

export function useThemePreference(scope: ThemeScope = 'admin') {
  const value = useContext(ThemePreferenceContext)
  if (!value) throw new Error('ThemePreferenceProvider is missing')
  return {
    themeId: scope === 'admin' ? value.adminThemeId : value.userThemeId,
    setTheme: (id: ConsoleThemeId) => value.setTheme(scope, id),
  }
}
