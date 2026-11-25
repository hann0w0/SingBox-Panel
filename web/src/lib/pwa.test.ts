import { describe, expect, it } from 'vitest'
import swSource from '../../public/sw.js?raw'
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

describe('public/sw.js navigation handling', () => {
  it('fetches navigate requests without RequestInit so WHATWG Fetch does not throw', async () => {
    const listeners = new Map<string, (event: any) => void>()
    const store = new Map<string, Map<string, Response>>()
    const cachesStub = {
      keys: async () => [...store.keys()],
      delete: async (name: string) => store.delete(name),
      open: async (name: string) => {
        let bucket = store.get(name)
        if (!bucket) {
          bucket = new Map()
          store.set(name, bucket)
        }
        return {
          match: async (key: Request | string) => bucket!.get(typeof key === 'string' ? key : key.url),
          put: async (key: Request | string, resp: Response) => {
            bucket!.set(typeof key === 'string' ? key : key.url, resp)
          },
          keys: async () => [...bucket!.keys()],
          delete: async (key: string) => bucket!.delete(key),
        }
      },
    }

    const fetchStub = async (input: any, init?: RequestInit) => {
      // WHATWG Fetch Standard: constructing/fetching a navigate Request with a
      // non-empty RequestInit synchronously throws TypeError in all browsers.
      if (input && typeof input === 'object' && input.mode === 'navigate' && init !== undefined) {
        throw new TypeError("Cannot construct a Request with a Request whose mode is 'navigate' and a non-empty RequestInit.")
      }
      return new Response('<!doctype html><title>SingBox Panel</title>', {
        status: 200,
        headers: { 'Content-Type': 'text/html; charset=utf-8' },
      })
    }

    const selfStub = {
      location: new URL('https://panel.example.com/sw.js'),
      skipWaiting: async () => {},
      clients: { claim: async () => {} },
      addEventListener: (type: string, fn: (event: any) => void) => listeners.set(type, fn),
    }
    // Evaluate the worker script in an isolated scope with spec-checked globals.
    const runWorker = new Function('self', 'caches', 'fetch', 'URL', 'Response', swSource)
    runWorker(selfStub, cachesStub, fetchStub, URL, Response)

    const onFetch = listeners.get('fetch')
    expect(onFetch).toBeTypeOf('function')

    let responded: Promise<Response> | undefined
    onFetch!({
      request: { method: 'GET', url: 'https://panel.example.com/admin/servers', mode: 'navigate' },
      respondWith: (p: Promise<Response>) => { responded = p },
    })
    expect(responded).toBeDefined()
    const res = await responded!
    expect(res.status).toBe(200)
    expect(await res.text()).toContain('SingBox Panel')
  })
})
