/* SingBox Panel service worker.
 *
 * Read this before editing — the caching rules are deliberate:
 *
 * 1. /api/ is never cached. The panel is an authenticated admin tool; API
 *    responses, the agent WebSocket and the SSE streams must always hit the
 *    network, so every /api/ request is passed straight through.
 * 2. /assets/* is served cache-first. Vite emits content-hashed bundle names,
 *    so a given URL can never change content and is safe to keep forever.
 * 3. index.html is never content-hashed and an in-place panel upgrade swaps
 *    which bundles it references. A stale shell therefore means a blank page
 *    pointing at deleted chunks, so navigations are network-first and the
 *    cached copy only answers when the backend is genuinely unreachable.
 * 4. Everything else (icons, manifest, logos) revalidates in the background.
 */
const VERSION = 'v1'
const SHELL_CACHE = `sbp-shell-${VERSION}`
const ASSET_CACHE = `sbp-assets-${VERSION}`
const CURRENT_CACHES = [SHELL_CACHE, ASSET_CACHE]
const SHELL_KEY = '/index.html'

// Last-resort page when the panel is unreachable and no shell has been cached
// yet (first run while offline). Kept inline: it must not need the network.
const OFFLINE_HTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>离线 · SingBox Panel</title>
<style>
  body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
       background:#f5f5f5;color:#1f1f1f;
       font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif}
  main{max-width:22rem;padding:2rem;text-align:center}
  h1{margin:0 0 .5rem;font-size:1.15rem}
  p{margin:0;color:#595959;font-size:.9rem;line-height:1.6}
</style></head>
<body><main><h1>面板暂时无法访问</h1>
<p>请检查网络连接，或稍后重试。服务恢复后此页面会自动加载最新版本。</p>
</main></body></html>`

self.addEventListener('install', (event) => {
  // No precache list: the shell is captured on the first navigation, and the
  // hashed chunks are captured as the page requests them.
  event.waitUntil(self.skipWaiting())
})

self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    const names = await caches.keys()
    await Promise.all(names.map((name) => (
      name.startsWith('sbp-') && !CURRENT_CACHES.includes(name) ? caches.delete(name) : undefined
    )))
    await self.clients.claim()
  })())
})

self.addEventListener('fetch', (event) => {
  const { request } = event
  if (request.method !== 'GET') return

  let url
  try {
    url = new URL(request.url)
  } catch {
    return
  }
  if (url.origin !== self.location.origin) return
  if (url.pathname === '/api' || url.pathname.startsWith('/api/')) return

  if (request.mode === 'navigate') {
    event.respondWith(handleNavigation(request))
    return
  }
  if (url.pathname.startsWith('/assets/')) {
    event.respondWith(cacheFirst(ASSET_CACHE, request))
    return
  }
  event.respondWith(staleWhileRevalidate(SHELL_CACHE, request))
})

async function handleNavigation(request) {
  try {
    const response = await fetch(request)
    // Only a real HTML shell is worth keeping; a plain-text "backend is
    // running" placeholder or an error page must not become the offline app.
    const type = response.headers.get('Content-Type') || ''
    if (response.ok && type.includes('text/html')) {
      const cache = await caches.open(SHELL_CACHE)
      await cache.put(SHELL_KEY, response.clone())
    }
    return response
  } catch {
    const cache = await caches.open(SHELL_CACHE)
    const cached = await cache.match(SHELL_KEY)
    if (cached) return cached
    return new Response(OFFLINE_HTML, {
      status: 503,
      headers: { 'Content-Type': 'text/html; charset=utf-8' },
    })
  }
}

async function cacheFirst(cacheName, request) {
  const cache = await caches.open(cacheName)
  const cached = await cache.match(request)
  if (cached) return cached
  try {
    const response = await fetch(request)
    if (response.ok) await cache.put(request, response.clone())
    return response
  } catch (error) {
    const retry = await cache.match(request)
    if (retry) return retry
    throw error
  }
}

async function staleWhileRevalidate(cacheName, request) {
  const cache = await caches.open(cacheName)
  const cached = await cache.match(request)
  const network = fetch(request).then(async (response) => {
    if (response.ok) await cache.put(request, response.clone())
    return response
  })
  if (cached) {
    // Refresh in the background; a failed refresh must not reject the response
    // the page is already getting.
    network.catch(() => {})
    return cached
  }
  try {
    return await network
  } catch {
    return Response.error()
  }
}
