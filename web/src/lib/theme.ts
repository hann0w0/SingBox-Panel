// Design "A": neutral, high-contrast, black primary actions.
const dayColors = {
  ink: '#111111', muted: '#666666', faint: '#8f8f8f', placeholder: '#a3a3a3',
  primary: '#111111', onPrimary: '#ffffff', link: '#0b63ce', brand: '#111111',
  border: '#eaeaea', borderStrong: '#d6d6d6', borderHover: '#b5b5b5',
  layout: '#ffffff', container: '#ffffff', elevated: '#ffffff',
  surface: '#fafafa', chrome: '#ffffff', chromeTint: '#fafafa', spotlight: '#111111',
  selected: '#f2f2f2', selectedBorder: '#e6e6e6', selectedText: '#111111',
  hover: '#f4f4f4', focus: '#0b63ce',
  success: '#16a34a', warning: '#d97706', error: '#dc2626',
  warningText: '#a35a00', errorText: '#b91c1c',
  download: '#111111', upload: '#9a9a9a',
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
      // Neutral greys (not pure black), close to the browser's own dark chrome.
      ink: '#ececec', muted: '#a8a8a8', faint: '#868686', placeholder: '#7a7a7a',
      primary: '#ececec', onPrimary: '#1e1e1e', link: '#7cb0ff', brand: '#ececec',
      border: '#353535', borderStrong: '#474747', borderHover: '#5e5e5e',
      layout: '#1e1e1e', container: '#1e1e1e', elevated: '#2a2a2a',
      surface: '#252525', chrome: '#1e1e1e', chromeTint: '#252525', spotlight: '#3a3a3a',
      selected: '#333333', selectedBorder: '#3d3d3d', selectedText: '#ececec',
      hover: '#2a2a2a', focus: '#7cb0ff',
      success: '#4cc46a', warning: '#e8a94a', error: '#f27a7c',
      warningText: '#e8a94a', errorText: '#f27a7c',
      download: '#ececec', upload: '#868686', shadow: '0 0 0',
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
