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

export class RouteErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('route render failed', error, info.componentStack)
  }

  componentDidUpdate(previous: Props) {
    if (previous.resetKey !== this.props.resetKey && this.state.error) {
      this.setState({ error: null })
    }
  }

  render() {
    if (!this.state.error) return this.props.children
    return (
      <div className="route-error" role="alert">
        <span className="route-error-mark" aria-hidden="true">!</span>
        <h1>页面加载失败</h1>
        <p className="mono">{this.state.error.message || '页面资源或运行状态异常'}</p>
        <div className="route-error-actions">
          <Button onClick={() => this.setState({ error: null })}>重试</Button>
          <Button onClick={() => window.location.reload()}>重新加载</Button>
          <Button type="primary" onClick={() => window.location.assign('/')}>返回首页</Button>
        </div>
      </div>
    )
  }
}
