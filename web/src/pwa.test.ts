import { describe, expect, it } from 'vitest'
import {
  DISPLAY_MODE_STANDALONE,
  isSecureContextForWorker,
  isStandalone,
  shouldRegisterServiceWorker,
} from './pwa'

// Minimal Window doubles: jsdom always reports a secure http://localhost
// origin, so the origin-dependent branches are driven by explicit stubs.
function fakeWindow(options: {
  protocol?: string
  hostname?: string
  standalone?: boolean
  matchMedia?: boolean
  serviceWorker?: boolean
}): Window {
  const {
    protocol = 'https:',
    hostname = 'node.hannn.de',
    standalone = false,
    matchMedia = true,
    serviceWorker = true,
  } = options
  const win = {
    location: { protocol, hostname },
    matchMedia: matchMedia
      ? (query: string) => ({ matches: standalone && query === DISPLAY_MODE_STANDALONE })
      : undefined,
    navigator: { standalone, ...(serviceWorker ? { serviceWorker: {} } : {}) },
  }
  return win as unknown as Window
}

describe('isStandalone', () => {
  it('detects the standalone display mode', () => {
    expect(isStandalone(fakeWindow({ standalone: true }))).toBe(true)
  })

  it('detects the legacy iOS navigator.standalone flag', () => {
    const win = fakeWindow({ matchMedia: false, standalone: true })
    expect(isStandalone(win)).toBe(true)
  })

  it('is false in a normal browser tab', () => {
    expect(isStandalone(fakeWindow({}))).toBe(false)
  })
})

describe('isSecureContextForWorker', () => {
  it('accepts https', () => {
    expect(isSecureContextForWorker(fakeWindow({ protocol: 'https:' }))).toBe(true)
  })

  it('accepts localhost over http so a local production preview can be tested', () => {
    expect(isSecureContextForWorker(fakeWindow({ protocol: 'http:', hostname: 'localhost' }))).toBe(true)
    expect(isSecureContextForWorker(fakeWindow({ protocol: 'http:', hostname: '127.0.0.1' }))).toBe(true)
  })

  it('rejects plain http on a public host', () => {
    expect(isSecureContextForWorker(fakeWindow({ protocol: 'http:', hostname: 'node.hannn.de' }))).toBe(false)
  })
})

describe('shouldRegisterServiceWorker', () => {
  it('registers only in a production build', () => {
    expect(shouldRegisterServiceWorker(fakeWindow({}), false)).toBe(false)
    expect(shouldRegisterServiceWorker(fakeWindow({}), true)).toBe(true)
  })

  it('skips browsers without service worker support', () => {
    expect(shouldRegisterServiceWorker(fakeWindow({ serviceWorker: false }), true)).toBe(false)
  })

  it('skips insecure origins', () => {
    const insecure = fakeWindow({ protocol: 'http:', hostname: 'node.hannn.de' })
    expect(shouldRegisterServiceWorker(insecure, true)).toBe(false)
  })
})
