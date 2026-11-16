/**
 * PWA plumbing: service-worker registration and the "add to home screen"
 * install prompt.
 *
 * Registration is production-only. The Vite dev server does not serve the
 * build output, so a worker registered there would cache a stale shell and
 * shadow hot reload.
 */
import { useCallback, useEffect, useState } from 'react'

export const DISPLAY_MODE_STANDALONE = '(display-mode: standalone)'

/** Chromium-only event that lets a page trigger the native install flow. */
export interface BeforeInstallPromptEvent extends Event {
  prompt: () => Promise<void>
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed'; platform: string }>
}

/**
 * True when the app runs as an installed app rather than in a browser tab.
 * iOS Safari predates the media query and only reports `navigator.standalone`.
 */
export function isStandalone(win: Window = window): boolean {
  if (typeof win.matchMedia === 'function' && win.matchMedia(DISPLAY_MODE_STANDALONE).matches) {
    return true
  }
  return (win.navigator as Navigator & { standalone?: boolean }).standalone === true
}

/**
 * Service workers need a secure context; localhost is the sanctioned exception
 * so a local production preview can be tested without TLS. Split out from the
 * PROD gate so the secure-context rule stays unit-testable.
 */
export function isSecureContextForWorker(win: Window = window): boolean {
  if (isStandalone(win)) return true
  const { protocol, hostname } = win.location
  return protocol === 'https:' || hostname === 'localhost' || hostname === '127.0.0.1'
}

export function shouldRegisterServiceWorker(win: Window, isProd: boolean): boolean {
  return isProd && 'serviceWorker' in win.navigator && isSecureContextForWorker(win)
}

/**
 * Registers /sw.js after the page has loaded, so the worker never competes with
 * the first paint. Returns a cleanup function.
 */
export function registerServiceWorker(win: Window = window): () => void {
  if (!shouldRegisterServiceWorker(win, import.meta.env.PROD)) return () => {}

  const register = () => {
    // `updateViaCache: 'none'` keeps the worker script itself out of the HTTP
    // cache, so an in-place panel upgrade is picked up on the next visit
    // instead of after the browser's 24h revalidation window.
    win.navigator.serviceWorker
      .register('/sw.js', { scope: '/', updateViaCache: 'none' })
      .catch((error) => console.warn('service worker registration failed', error))
  }

  if (win.document.readyState === 'complete') {
    register()
    return () => {}
  }
  win.addEventListener('load', register, { once: true })
  return () => win.removeEventListener('load', register)
}

/** Marks the document so CSS can react to standalone mode. There is no CSS
 * media query for iOS's legacy `navigator.standalone`, so the layout needs the
 * flag to add safe-area padding. */
export function markStandalone(win: Window = window): void {
  if (isStandalone(win)) win.document.documentElement.dataset.standalone = 'true'
}

/**
 * Exposes the deferred install prompt. Chrome fires `beforeinstallprompt` while
 * the panel is installable and suppresses its own UI until the event is used,
 * so the panel renders its own entry point and calls `install()` on tap.
 *
 * `canInstall` stays false on iOS (no such event) and once the app is already
 * installed, which is exactly when the button should be hidden.
 */
export function useInstallPrompt(win: Window = window): {
  canInstall: boolean
  install: () => Promise<'accepted' | 'dismissed'>
} {
  const [promptEvent, setPromptEvent] = useState<BeforeInstallPromptEvent | null>(null)

  useEffect(() => {
    if (isStandalone(win)) return
    const onPrompt = (event: Event) => {
      // Preventing the default keeps the browser's own mini-infobar away so the
      // panel controls when installation is offered.
      event.preventDefault()
      setPromptEvent(event as BeforeInstallPromptEvent)
    }
    const onInstalled = () => setPromptEvent(null)
    win.addEventListener('beforeinstallprompt', onPrompt)
    win.addEventListener('appinstalled', onInstalled)
    return () => {
      win.removeEventListener('beforeinstallprompt', onPrompt)
      win.removeEventListener('appinstalled', onInstalled)
    }
  }, [win])

  const install = useCallback(async () => {
    if (!promptEvent) return 'dismissed' as const
    await promptEvent.prompt()
    const choice = await promptEvent.userChoice
    setPromptEvent(null)
    return choice.outcome === 'accepted' ? ('accepted' as const) : ('dismissed' as const)
  }, [promptEvent])

  return { canInstall: promptEvent !== null, install }
}
