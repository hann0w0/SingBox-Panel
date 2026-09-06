import type { ThemeConfig } from 'antd'

export const loginTheme: ThemeConfig = {
  token: { colorPrimary: '#3a5bff', borderRadius: 8 },
}

const warmColors = {
  ink: '#252422', muted: '#59554f', faint: '#69645d', placeholder: '#736e66',
  primary: '#353432', link: '#55514b', brand: '#94652e',
  border: '#e6e2db', borderStrong: '#dedbd5', borderHover: '#b8afa0',
  layout: '#f5f4f1', surface: '#f8f7f4', chrome: '#fcfbf9', chromeTint: '#fcfbf9',
  selected: '#eeece6', selectedBorder: '#e2ded5', selectedText: '#302f2d',
  hover: '#f3f1ec', focus: '#777064',
  success: '#42644b', warning: '#91611f', error: '#a3423d',
  download: '#484e43', upload: '#876238',
  shadow: '40 36 30',
}

export type ConsoleThemeId = 'warm' | 'blue' | 'green'
export type ConsolePalette = {
  id: ConsoleThemeId
  name: string
  colors: Record<keyof typeof warmColors, string>
}

export const consolePalettes: ConsolePalette[] = [
  {
    id: 'blue', name: '经典',
    colors: {
      ...warmColors,
      // Match v1.0's white cards, neutral canvas and vivid status colors.
      ink: '#1f1f1f', muted: '#595959', faint: '#666666', placeholder: '#737373',
      primary: '#3a5bff', link: '#1677ff', brand: '#3a5bff',
      border: '#f0f0f0', borderStrong: '#d9d9d9', borderHover: '#d9d9d9',
      layout: '#f5f5f5', surface: '#fafafa', chrome: '#ffffff', chromeTint: '#ffffff',
      selected: '#f0f5ff', selectedBorder: '#dee8ff', selectedText: '#3a5bff',
      hover: '#f5f5f5', focus: '#3a5bff',
      success: '#52c41a', warning: '#faad14', error: '#ff4d4f',
      download: '#4096ff', upload: '#36cfc9', shadow: '0 0 0',
    },
  },
  { id: 'warm', name: '暖灰', colors: warmColors },
  {
    id: 'green', name: '青绿',
    colors: {
      ...warmColors,
      ink: '#20372e', muted: '#4f655a', faint: '#5d7065', placeholder: '#6c7c72',
      primary: '#247668', link: '#236f62', brand: '#247668',
      border: '#dfe9e3', borderStrong: '#d1e0d7', borderHover: '#91b8a6',
      layout: '#f1f6f3', surface: '#f4f8f5', chrome: '#f9fcfa', chromeTint: '#f0f8f5',
      selected: '#e3f0e9', selectedBorder: '#cce2d7', selectedText: '#236f62',
      hover: '#edf5f0', focus: '#3c8b78',
      download: '#247668', upload: '#527532', shadow: '33 71 54',
    },
  },
]

export const DEFAULT_CONSOLE_THEME: ConsoleThemeId = 'blue'
export const DEFAULT_USER_CONSOLE_THEME: ConsoleThemeId = 'warm'
export const userConsolePalettes = consolePalettes.filter((p) => p.id === 'blue' || p.id === 'warm')

export function parseConsoleTheme(value: unknown): ConsoleThemeId {
  return consolePalettes.find((p) => p.id === value)?.id ?? DEFAULT_CONSOLE_THEME
}

export function parseUserConsoleTheme(value: unknown): ConsoleThemeId {
  return userConsolePalettes.find((p) => p.id === value)?.id ?? DEFAULT_USER_CONSOLE_THEME
}

export function getConsolePalette(id: ConsoleThemeId): ConsolePalette {
  return consolePalettes.find((p) => p.id === parseConsoleTheme(id))!
}

export function consoleCssVariables(palette: ConsolePalette): Record<string, string> {
  return Object.fromEntries(Object.entries(palette.colors).map(([name, value]) => [
    `--console-${name.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)}`, value,
  ]))
}

export function getConsoleTheme(palette: ConsolePalette): ThemeConfig {
  const c = palette.colors
  const isClassic = palette.id === 'blue'
  return {
    token: {
      colorPrimary: c.primary, colorInfo: c.link,
      colorPrimaryBg: c.selected, colorPrimaryBgHover: c.selectedBorder,
      controlItemBgActive: c.selected, controlItemBgActiveHover: c.selectedBorder,
      controlItemBgHover: c.hover,
      colorSuccess: c.success, colorWarning: c.warning, colorError: c.error,
      colorLink: c.link, colorLinkHover: c.primary,
      colorText: c.ink, colorTextSecondary: c.muted, colorTextTertiary: c.faint,
      colorTextPlaceholder: c.placeholder,
      colorBgLayout: c.layout, colorBgContainer: '#ffffff',
      colorBorder: c.borderStrong, colorBorderSecondary: c.border,
      colorFillAlter: c.surface, colorFillSecondary: c.selected,
      borderRadius: 8, borderRadiusLG: 12, controlHeight: 36, fontSize: 14,
      boxShadow: `0 8px 28px rgb(${c.shadow} / 8%)`,
      boxShadowSecondary: `0 12px 40px rgb(${c.shadow} / 12%)`,
      motionDurationFast: '0.12s', motionDurationMid: '0.2s', motionDurationSlow: '0.28s',
    },
    components: {
      Layout: { bodyBg: c.layout, headerBg: '#ffffff', lightSiderBg: c.chrome },
      Menu: {
        itemBg: c.chrome, subMenuItemBg: c.chrome, itemColor: isClassic ? c.ink : c.muted,
        itemHoverColor: c.selectedText, itemHoverBg: c.hover,
        itemSelectedBg: c.selected, itemSelectedColor: c.selectedText,
        itemHeight: 44, itemBorderRadius: 8,
      },
      Button: { primaryShadow: 'none', defaultShadow: 'none', fontWeight: 500 },
      Card: { headerFontSize: 15, headerHeight: 54 },
      Table: {
        headerBg: c.surface, headerColor: isClassic ? c.ink : c.muted, headerSplitColor: c.border,
        rowHoverBg: c.surface, rowSelectedBg: c.selected, rowSelectedHoverBg: c.hover,
        borderColor: c.border,
      },
      Tabs: { inkBarColor: c.primary, itemSelectedColor: c.primary, itemHoverColor: c.link },
      Segmented: { trackBg: c.selected, itemSelectedBg: '#ffffff', itemSelectedColor: c.selectedText },
      Select: { optionSelectedBg: c.selected, optionActiveBg: c.hover },
      Progress: { defaultColor: c.success, remainingColor: isClassic ? '#f0f0f0' : c.selected },
      Statistic: { contentFontSize: 28, titleFontSize: 13 },
      Tooltip: { colorBgSpotlight: c.ink },
    },
  }
}
