import { Component, type ErrorInfo, type ReactNode } from 'react'
import { Button } from './kit'
import '../styles/settings-user.css'

interface Props {
  children: ReactNode
  resetKey: string
}

interface State {
  error: Error | null
}

const CHUNK_RELOAD_COOLDOWN_MS = 30_000

export function isChunkLoadError(error: Error | null | undefined): boolean {
  if (!error) return false
  const msg = `${error.name || ''} ${error.message || ''}`
  return /Failed to fetch dynamically imported module|Importing a module script failed|error loading dynamically imported module|Loading chunk [\d\w-]+ failed|ChunkLoadError/i.test(msg)
}

export class RouteErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('route render failed', error, info.componentStack)
    if (isChunkLoadError(error) && typeof window !== 'undefined') {
      try {
        const key = `sbp-chunk-reload:${window.location.pathname}`
        const last = Number(window.sessionStorage.getItem(key) || '0')
        if (!Number.isFinite(last) || Date.now() - last > CHUNK_RELOAD_COOLDOWN_MS) {
          window.sessionStorage.setItem(key, String(Date.now()))
          window.location.reload()
        }
      } catch {
        // Ignore sessionStorage restrictions.
      }
    }
  }

  componentDidUpdate(previous: Props) {
    if (previous.resetKey !== this.props.resetKey && this.state.error) {
      this.setState({ error: null })
    }
  }

  render() {
    if (!this.state.error) return this.props.children
    const chunkErr = isChunkLoadError(this.state.error)
    return (
      <div className="route-error" role="alert">
        <span className="route-error-mark" aria-hidden="true">!</span>
        <h1>页面加载失败</h1>
        <p className="mono">{this.state.error.message || '页面资源或运行状态异常'}</p>
        <div className="route-error-actions">
          <Button onClick={() => { if (chunkErr) window.location.reload(); else this.setState({ error: null }) }}>重试</Button>
          <Button onClick={() => window.location.reload()}>重新加载</Button>
          <Button type="primary" onClick={() => window.location.assign('/')}>返回首页</Button>
        </div>
      </div>
    )
  }
}
