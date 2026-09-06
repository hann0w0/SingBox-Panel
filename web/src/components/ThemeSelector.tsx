import { useState } from 'react'
import { Button, Dropdown } from 'antd'
import { CheckOutlined, DesktopOutlined, DownOutlined, MoonOutlined, SunOutlined } from '@ant-design/icons'
import { consolePalettes, userConsolePalettes, getConsolePalette, parseConsoleTheme } from '../theme'
import { useThemePreference, type ThemeScope } from '../themePreference'

export default function ThemeSelector({ scope = 'admin', variant = 'sidebar', compact = false }: {
  scope?: ThemeScope
  variant?: 'sidebar' | 'account'
  compact?: boolean
}) {
  const { themeId, resolvedThemeId, setTheme } = useThemePreference(scope)
  const palette = getConsolePalette(resolvedThemeId)
  const palettes = scope === 'user' ? userConsolePalettes : consolePalettes
  const options = [{ id: 'auto', name: '自动（跟随系统）' }, ...palettes]
  const name = themeId === 'auto' ? '自动' : palette.name
  const description = themeId === 'auto' ? `自动（当前：${palette.name}）` : name
  const [open, setOpen] = useState(false)
  return (
    <Dropdown
      trigger={['click']}
      placement={variant === 'account' ? 'bottomRight' : 'topLeft'}
      open={open}
      onOpenChange={setOpen}
      overlayClassName="console-theme-dropdown"
      menu={{
        selectable: true,
        selectedKeys: [themeId],
        onClick: ({ key }) => { setTheme(parseConsoleTheme(key)); setOpen(false) },
        items: options.map((p) => ({
          key: p.id,
          label: (
            <span className="console-theme-option">
              <span className="console-theme-icon" aria-hidden="true">
                {p.id === 'auto' ? <DesktopOutlined /> : p.id === 'dark' ? <MoonOutlined /> : <SunOutlined />}
              </span>
              <span>{p.name}</span>
              {themeId === p.id && <CheckOutlined className="console-theme-check" />}
            </span>
          ),
        })),
      }}
    >
      <Button
        className={`console-theme-trigger${variant === 'account' ? ' console-theme-account' : ''}${compact ? ' is-compact' : ''}`}
        title={`主题：${description}`}
        aria-label={`切换主题，当前：${description}`}
        aria-haspopup="menu"
        aria-expanded={open}
      >
        <span className="console-theme-icon" aria-hidden="true">
          {themeId === 'auto' ? <DesktopOutlined /> : themeId === 'dark' ? <MoonOutlined /> : <SunOutlined />}
        </span>
        {variant === 'sidebar' && <span>主题</span>}
        {!compact && <span className="console-theme-name">{name}</span>}
        {!compact && <DownOutlined />}
      </Button>
    </Dropdown>
  )
}
