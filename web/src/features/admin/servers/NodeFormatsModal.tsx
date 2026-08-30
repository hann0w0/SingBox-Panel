import { useEffect, useRef, useState } from 'react'
import { Button, Descriptions, List, Modal, Space, Tag, Tooltip, Typography, message } from 'antd'
import { CheckOutlined, CopyOutlined, EyeOutlined } from '@ant-design/icons'
import type { NodeFormats } from '../../../api'
import { copyToClipboard } from '../../../util'

const PROTOCOL_COLORS: Record<string, string> = {
  vless: 'blue', vmess: 'geekblue', trojan: 'purple', shadowsocks: 'cyan',
  hysteria2: 'orange', hysteria: 'volcano', tuic: 'gold', anytls: 'magenta',
  snell: 'green', naive: 'lime', socks: 'default',
}

export function NodeFormatsModalContent({ data }: { data: NodeFormats }) {
  const [copiedKey, setCopiedKey] = useState<string | null>(null)
  const [detailItem, setDetailItem] = useState<NodeFormats['items'][number] | null>(null)
  const copiedTimerRef = useRef<number | null>(null)
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      if (copiedTimerRef.current !== null) window.clearTimeout(copiedTimerRef.current)
    }
  }, [])

  const copy = (text: string, key: string, label: string) => {
    if (!text) return
    copyToClipboard(text)
      .then(() => {
        if (!mountedRef.current) return
        setCopiedKey(key)
        message.success(`已复制 ${label}`)
        if (copiedTimerRef.current !== null) window.clearTimeout(copiedTimerRef.current)
        copiedTimerRef.current = window.setTimeout(() => {
          copiedTimerRef.current = null
          setCopiedKey(null)
        }, 1800)
      })
      .catch(() => message.error('复制失败，请手动选择内容'))
  }

  const items = data.items || []
  return (
    <div style={{ paddingTop: 4 }}>
      <div style={{ fontSize: 13, color: 'rgba(0, 0, 0, 0.45)', marginBottom: 12 }}>
        点击“详情”查看节点参数；多用户协议请使用具体用户的订阅。
      </div>
      {!items.length ? (
        <div style={{ padding: '36px 0', textAlign: 'center', color: '#94a3b8' }}>暂无可导出的单凭证入站</div>
      ) : (
        <List
          size="small"
          bordered
          dataSource={items}
          renderItem={(item, index) => {
            const displayTag = item.tag || item.name || ''
            const buttons = [
              { key: `uri-${index}`, label: 'URI', value: item.uri },
              { key: `clash-${index}`, label: 'Clash', value: item.clash },
              { key: `surge-${index}`, label: 'Surge', value: item.surge },
            ]
            return (
              <List.Item style={{ padding: '12px 16px', display: 'flex', flexWrap: 'wrap', gap: 12 }}>
                <span style={{ fontWeight: 500, color: '#1e293b', wordBreak: 'break-all' }}>{displayTag}</span>
                <Space size={8} wrap>
                  <Button size="small" icon={<EyeOutlined />} onClick={() => setDetailItem(item)}>详情</Button>
                  {buttons.map((button) => button.value ? (
                    <Button
                      key={button.key}
                      size="small"
                      type={copiedKey === button.key ? 'primary' : 'default'}
                      icon={copiedKey === button.key ? <CheckOutlined /> : <CopyOutlined />}
                      onClick={() => copy(button.value, button.key, `${displayTag} ${button.label}`)}
                    >
                      {button.label}
                    </Button>
                  ) : (
                    <Tooltip key={button.key} title={`${button.label} 不支持该协议`}>
                      <Button size="small" disabled icon={<CopyOutlined />}>{button.label}</Button>
                    </Tooltip>
                  ))}
                </Space>
              </List.Item>
            )
          }}
        />
      )}

      <Modal
        title={`${detailItem?.tag || detailItem?.name || ''} 节点详情`}
        open={!!detailItem}
        onCancel={() => setDetailItem(null)}
        footer={<Button onClick={() => setDetailItem(null)}>关闭</Button>}
        width={620}
        destroyOnClose
      >
        {detailItem && (
          <Descriptions bordered size="small" column={1}>
            <Descriptions.Item label="节点地址 / IP"><Typography.Text copyable>{detailItem.server || '—'}</Typography.Text></Descriptions.Item>
            <Descriptions.Item label="端口"><Typography.Text copyable>{String(detailItem.port)}</Typography.Text></Descriptions.Item>
            <Descriptions.Item label="协议"><Tag color={PROTOCOL_COLORS[detailItem.type] || 'default'}>{detailItem.type}</Tag></Descriptions.Item>
            {Object.entries(detailItem.params || {})
              .filter(([key]) => !['服务器', '端口', '协议'].includes(key))
              .map(([key, value]) => (
                <Descriptions.Item key={key} label={key}>
                  <Typography.Text copyable={{ text: value }}>{value || '—'}</Typography.Text>
                </Descriptions.Item>
              ))}
          </Descriptions>
        )}
      </Modal>
    </div>
  )
}
