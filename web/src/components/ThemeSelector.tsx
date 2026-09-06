import { useState } from 'react'
import { Button, Dropdown } from 'antd'
import { CheckOutlined, DownOutlined } from '@ant-design/icons'
import { consolePalettes, userConsolePalettes, getConsolePalette, parseConsoleTheme } from '../theme'
import { useThemePreference, type ThemeScope } from '../themePreference'

export default function ThemeSelector({ scope = 'admin', variant = 'sidebar', compact = false }: {
  scope?: ThemeScope
  variant?: 'sidebar' | 'account'
  compact?: boolean
}) {
  const { themeId, setTheme } = useThemePreference(scope)
  const palette = getConsolePalette(themeId)
  const palettes = scope === 'user' ? userConsolePalettes : consolePalettes
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
        items: palettes.map((p) => ({
          key: p.id,
          label: (
            <span className="console-theme-option">
              <span className="console-theme-swatches" aria-hidden="true">
                <i style={{ background: p.colors.primary }} />
                <i style={{ background: p.colors.upload }} />
                <i style={{ background: p.colors.selected }} />
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
        title={`主题：${palette.name}`}
        aria-label={`切换主题，当前：${palette.name}`}
        aria-haspopup="menu"
        aria-expanded={open}
      >
        <span className="console-theme-dot" aria-hidden="true" style={{ background: `linear-gradient(135deg, ${palette.colors.primary} 50%, ${palette.colors.upload} 50%)` }} />
        {variant === 'sidebar' && <span>主题</span>}
        {!compact && <span className="console-theme-name">{palette.name}</span>}
        {!compact && <DownOutlined />}
      </Button>
    </Dropdown>
  )
}
