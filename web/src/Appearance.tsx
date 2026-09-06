import { useEffect, useLayoutEffect, useMemo, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { App as AntApp, ConfigProvider } from 'antd'
import zhCN from 'antd/es/locale/zh_CN'
import { consoleCssVariables, getConsolePalette, getConsoleTheme, loginTheme } from './theme'
import { useThemePreference } from './themePreference'
import { useAuth } from './store'
import App from './App'

export default function Appearance() {
  const { pathname } = useLocation()
  const isLogin = pathname === '/login'
  const role = useAuth((state) => state.user?.role)
  const { themeId } = useThemePreference(role === 'admin' ? 'admin' : 'user')
  const palette = getConsolePalette(themeId)
  const [reducedMotion, setReducedMotion] = useState(() => window.matchMedia('(prefers-reduced-motion: reduce)').matches)

  useEffect(() => {
    const media = window.matchMedia('(prefers-reduced-motion: reduce)')
    const update = () => setReducedMotion(media.matches)
    media.addEventListener('change', update)
    return () => media.removeEventListener('change', update)
  }, [])

  useLayoutEffect(() => {
    if (isLogin) return
    const root = document.documentElement
    const variables = consoleCssVariables(palette)
    root.dataset.consoleTheme = palette.id
    Object.entries(variables).forEach(([name, value]) => root.style.setProperty(name, value))
    return () => {
      delete root.dataset.consoleTheme
      Object.keys(variables).forEach((name) => root.style.removeProperty(name))
    }
  }, [isLogin, palette])

  const activeTheme = useMemo(() => {
    if (isLogin) return loginTheme
    const config = getConsoleTheme(palette)
    return { ...config, token: { ...config.token, motion: !reducedMotion } }
  }, [isLogin, palette, reducedMotion])

  // Static confirmation dialogs and notifications use their own React roots.
  useEffect(() => {
    ConfigProvider.config({
      holderRender: (children) => (
        <ConfigProvider locale={zhCN} theme={activeTheme}>
          <div className={isLogin ? undefined : 'console-app'}>{children}</div>
        </ConfigProvider>
      ),
    })
  }, [activeTheme, isLogin])

  return (
    <ConfigProvider locale={zhCN} theme={activeTheme}>
      <AntApp className={isLogin ? undefined : 'console-app'}>
        <App />
      </AntApp>
    </ConfigProvider>
  )
}
