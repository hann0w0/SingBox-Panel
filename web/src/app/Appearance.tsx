import { useLayoutEffect } from 'react'
import { useLocation } from 'react-router-dom'
import { TooltipProvider } from '../components/ui/tooltip'
import { consoleCssVariables, getConsolePalette } from '../lib/theme'
import { useThemePreference } from '../lib/themePreference'
import { useAuth } from '../lib/store'
import App from '../App'

export default function Appearance() {
  const { pathname } = useLocation()
  const role = useAuth(state => state.user?.role)
  const { resolvedThemeId } = useThemePreference(role === 'admin' ? 'admin' : 'user')
  const palette = getConsolePalette(pathname === '/login' ? 'blue' : resolvedThemeId)
  useLayoutEffect(() => {
    const root = document.documentElement
    const variables = consoleCssVariables(palette)
    root.dataset.consoleTheme = palette.id
    root.classList.toggle('dark', palette.id === 'dark')
    Object.entries(variables).forEach(([name, value]) => root.style.setProperty(name, value))
    return () => {
      delete root.dataset.consoleTheme
      root.classList.remove('dark')
      Object.keys(variables).forEach(name => root.style.removeProperty(name))
    }
  }, [palette])
  return <TooltipProvider delayDuration={300}><div className="console-app"><App /></div></TooltipProvider>
}
