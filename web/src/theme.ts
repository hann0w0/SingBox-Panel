import { theme, type ThemeConfig } from 'antd'

export const loginTheme: ThemeConfig = {
  token: { colorPrimary: '#3a5bff', borderRadius: 8 },
}

const dayColors = {
  ink: '#1f1f1f', muted: '#595959', faint: '#666666', placeholder: '#737373',
  primary: '#3a5bff', onPrimary: '#ffffff', link: '#1677ff', brand: '#3a5bff',
  border: '#f0f0f0', borderStrong: '#d9d9d9', borderHover: '#d9d9d9',
  layout: '#f5f5f5', container: '#ffffff', elevated: '#ffffff',
  surface: '#fafafa', chrome: '#ffffff', chromeTint: '#ffffff', spotlight: '#1f1f1f',
  selected: '#f0f5ff', selectedBorder: '#dee8ff', selectedText: '#3a5bff',
  hover: '#f5f5f5', focus: '#3a5bff',
  success: '#52c41a', warning: '#faad14', error: '#ff4d4f',
  warningText: '#ad6800', errorText: '#a61d24',
  download: '#4096ff', upload: '#36cfc9',
  shadow: '0 0 0',
}

export type ConsoleThemeId = 'blue' | 'dark'
export type ConsoleThemePreference = ConsoleThemeId | 'auto'
export type ConsolePalette = {
  id: ConsoleThemeId
  name: string
  colors: Record<keyof typeof dayColors, string>
}

export const consolePalettes: ConsolePalette[] = [
  // Keep the existing ID so saved classic preferences become the day theme.
  { id: 'blue', name: '日间', colors: dayColors },
  {
    id: 'dark', name: '夜间',
    colors: {
      // Neutral surfaces sampled from the browser toolbar and address bar.
      ink: '#f1f1f1', muted: '#c2c2c2', faint: '#aaaaaa', placeholder: '#979797',
      primary: '#8ab4f8', onPrimary: '#1b1b1b', link: '#9fc6ff', brand: '#adcfff',
      border: '#3b3b3b', borderStrong: '#515151', borderHover: '#747474',
      layout: '#1b1b1b', container: '#262626', elevated: '#303030',
      surface: '#2d2d2d', chrome: '#262626', chromeTint: '#262626', spotlight: '#454545',
      selected: '#3a3f47', selectedBorder: '#505965', selectedText: '#c2dcff',
      hover: '#363636', focus: '#a8c7fa',
      success: '#9fd4ac', warning: '#edc985', error: '#f2aaa6',
      warningText: '#edc985', errorText: '#f2aaa6',
      download: '#9fc6ff', upload: '#8ed1c6', shadow: '0 0 0',
    },
  },
]

export const DEFAULT_CONSOLE_THEME: ConsoleThemeId = 'blue'
export const DEFAULT_USER_CONSOLE_THEME: ConsoleThemeId = DEFAULT_CONSOLE_THEME
export const userConsolePalettes = consolePalettes

export function parseConsoleTheme(value: unknown): ConsoleThemePreference {
  if (value === 'auto') return 'auto'
  return consolePalettes.find((p) => p.id === value)?.id ?? DEFAULT_CONSOLE_THEME
}

export function parseUserConsoleTheme(value: unknown): ConsoleThemePreference {
  return parseConsoleTheme(value)
}

export function getConsolePalette(id: ConsoleThemeId): ConsolePalette {
  return consolePalettes.find((p) => p.id === id) ?? consolePalettes[0]
}

export function consoleCssVariables(palette: ConsolePalette): Record<string, string> {
  return Object.fromEntries(Object.entries(palette.colors).map(([name, value]) => [
    `--console-${name.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)}`, value,
  ]))
}

export function getConsoleTheme(palette: ConsolePalette): ThemeConfig {
  const c = palette.colors
  return {
    algorithm: palette.id === 'dark' ? theme.darkAlgorithm : theme.defaultAlgorithm,
    token: {
      colorPrimary: c.primary, colorInfo: c.link,
      colorPrimaryBg: c.selected, colorPrimaryBgHover: c.selectedBorder,
      controlItemBgActive: c.selected, controlItemBgActiveHover: c.selectedBorder,
      controlItemBgHover: c.hover,
      colorSuccess: c.success, colorWarning: c.warning, colorError: c.error,
      colorLink: c.link, colorLinkHover: c.primary,
      colorText: c.ink, colorTextSecondary: c.muted, colorTextTertiary: c.faint,
      colorTextPlaceholder: c.placeholder,
      colorBgLayout: c.layout, colorBgContainer: c.container, colorBgElevated: c.elevated,
      colorBorder: c.borderStrong, colorBorderSecondary: c.border,
      colorFillAlter: c.surface, colorFillSecondary: c.selected,
      borderRadius: 8, borderRadiusLG: 12, controlHeight: 36, fontSize: 14,
      boxShadow: `0 8px 28px rgb(${c.shadow} / 8%)`,
      boxShadowSecondary: `0 12px 40px rgb(${c.shadow} / 12%)`,
      motionDurationFast: '0.12s', motionDurationMid: '0.2s', motionDurationSlow: '0.28s',
    },
    components: {
      Layout: { bodyBg: c.layout, headerBg: c.chrome, lightSiderBg: c.chrome },
      Menu: {
        itemBg: c.chrome, subMenuItemBg: c.chrome, itemColor: c.ink,
        itemHoverColor: c.selectedText, itemHoverBg: c.hover,
        itemSelectedBg: c.selected, itemSelectedColor: c.selectedText,
        itemHeight: 44, itemBorderRadius: 8,
      },
      Button: {
        primaryShadow: 'none', defaultShadow: 'none', fontWeight: 500,
        primaryColor: c.onPrimary, dangerColor: c.onPrimary,
      },
      Card: { headerFontSize: 15, headerHeight: 54 },
      Table: {
        headerBg: c.surface, headerColor: c.ink, headerSplitColor: c.border,
        rowHoverBg: c.surface, rowSelectedBg: c.selected, rowSelectedHoverBg: c.hover,
        borderColor: c.border,
      },
      Tabs: { inkBarColor: c.primary, itemSelectedColor: c.primary, itemHoverColor: c.link },
      Segmented: { trackBg: c.selected, itemSelectedBg: c.container, itemSelectedColor: c.selectedText },
      Select: { optionSelectedBg: c.selected, optionActiveBg: c.hover },
      Progress: { defaultColor: c.success, remainingColor: c.border },
      Statistic: { contentFontSize: 28, titleFontSize: 13 },
      Tooltip: { colorBgSpotlight: c.spotlight },
    },
  }
}
