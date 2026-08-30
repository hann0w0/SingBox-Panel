import { Component, type ErrorInfo, type ReactNode } from 'react'
import { Button, Result, Space } from 'antd'
import { HomeOutlined, ReloadOutlined } from '@ant-design/icons'

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
      <Result
        status="error"
        title="页面加载失败"
        subTitle={this.state.error.message || '页面资源或运行状态异常'}
        extra={(
          <Space wrap>
            <Button icon={<ReloadOutlined />} onClick={() => this.setState({ error: null })}>重试</Button>
            <Button icon={<ReloadOutlined />} onClick={() => window.location.reload()}>重新加载</Button>
            <Button type="primary" icon={<HomeOutlined />} onClick={() => window.location.assign('/')}>返回首页</Button>
          </Space>
        )}
      />
    )
  }
}
