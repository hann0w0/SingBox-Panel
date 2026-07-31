import { useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Col,
  Descriptions,
  Form,
  Input,
  Modal,
  Row,
  Space,
  Statistic,
  Table,
  Tag,
  Typography,
  message,
} from 'antd'
import { QRCodeSVG } from 'qrcode.react'
import { changePassword, errMsg, getMe, getUserNodes, resetSub } from '../../api'
import type { UserNode } from '../../api'
import type { User } from '../../types'
import { copyToClipboard, formatDate } from '../../util'
import { useAuth } from '../../store'

// Per-client brand accent colors for the subscription buttons.
const SUB_STYLES = {
  clash: { background: '#16a34a', borderColor: '#16a34a' },
  shadowrocket: { background: '#7c3aed', borderColor: '#7c3aed' },
  surge: { background: '#0ea5e9', borderColor: '#0ea5e9' },
}

const LOGO_VERSION = '20260727-3'

// The version query prevents a previously cached failed response from keeping
// the old placeholder visible after an upgrade.
function ClientLogo({ src, monochrome = false }: { src: string; monochrome?: boolean }) {
  return (
    <img
      src={`${src}?v=${LOGO_VERSION}`}
      alt=""
      style={{
        width: '1.4em',
        height: '1.4em',
        objectFit: 'contain',
        verticalAlign: '-0.28em',
        filter: monochrome ? 'brightness(0) invert(1)' : undefined,
      }}
    />
  )
}

export default function Dashboard() {
  const setAuth = useAuth((s) => s.setAuth)
  const setUser = useAuth((s) => s.setUser)
  const [user, setLocalUser] = useState<User | null>(null)
  const [subUrl, setSubUrl] = useState('')
  const [nodes, setNodes] = useState<UserNode[]>([])
  const [selected, setSelected] = useState<UserNode | null>(null)
  const [pwdOpen, setPwdOpen] = useState(false)
  const [pwdForm] = Form.useForm()

  const load = () => {
    getMe()
      .then((d) => {
        setLocalUser(d.user)
        setSubUrl(d.subscription_url)
        setUser(d.user)
      })
      .catch((e) => message.error(errMsg(e)))
    getUserNodes().then(setNodes).catch(() => {})
  }
  useEffect(load, [])

  if (!user) return null

  const link = (target: string) => (target ? `${subUrl}?target=${target}` : subUrl)

  const copySub = async (target: string, label: string) => {
    try {
      await copyToClipboard(link(target))
      message.success(`已复制 ${label} 订阅链接`)
    } catch {
      // Copying can be blocked by the browser; show the link so it is never a
      // dead end — the user can select it manually.
      Modal.info({
        title: `${label} 订阅链接`,
        width: 640,
        content: (
          <Typography.Paragraph copyable code style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
            {link(target)}
          </Typography.Paragraph>
        ),
      })
    }
  }

  const doReset = () =>
    Modal.confirm({
      title: '重置订阅链接?',
      content: '旧链接将立即失效，需要在客户端重新导入。',
      onOk: async () => {
        const r = await resetSub()
        setSubUrl(r.subscription_url)
        message.success('已重置')
      },
    })

  const doChangePwd = async () => {
    const v = await pwdForm.validateFields()
    try {
      const result = await changePassword(v.old_password, v.new_password)
      setAuth(result.token, user)
      message.success('密码已修改')
      setPwdOpen(false)
      pwdForm.resetFields()
    } catch (e) {
      message.error(errMsg(e))
    }
  }

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Row gutter={[16, 16]}>
        <Col xs={24}>
          <Card style={{ height: '100%' }}>
            <Statistic
              title="到期时间"
              value=" "
              formatter={() => (user.expire_at ? formatDate(user.expire_at) : '永不过期')}
            />
            <Tag color={user.enabled ? 'green' : 'red'} style={{ marginTop: 8 }}>
              {user.enabled ? '正常' : '已停用'}
            </Tag>
          </Card>
        </Col>
      </Row>

      <Card title="订阅链接">
        <div style={{ color: '#888', marginBottom: 12 }}>点击按钮复制对应客户端的订阅链接</div>
        <Space wrap>
          <Button
            type="primary"
            icon={<ClientLogo src="/logos/clashmeta.png" />}
            style={SUB_STYLES.clash}
            onClick={() => copySub('clash', 'ClashMeta')}
          >
            ClashMeta
          </Button>
          <Button
            type="primary"
            icon={<ClientLogo src="/logos/shadowrocket.png" monochrome />}
            style={SUB_STYLES.shadowrocket}
            onClick={() => copySub('', 'Shadowrocket')}
          >
            Shadowrocket
          </Button>
          <Button
            type="primary"
            icon={<ClientLogo src="/logos/surge.png" monochrome />}
            style={SUB_STYLES.surge}
            onClick={() => copySub('surge', 'Surge')}
          >
            Surge
          </Button>
        </Space>
        <div style={{ marginTop: 20 }}>
          <Space>
            <Button onClick={doReset}>重置订阅链接</Button>
            <Button onClick={() => setPwdOpen(true)}>修改密码</Button>
          </Space>
        </div>
      </Card>

      <Card title={`可用节点（${nodes.length}）`}>
        <Table
          rowKey={(n) => `${n.name}-${n.server}-${n.port}`}
          size="small"
          pagination={false}
          scroll={{ x: 520 }}
          dataSource={nodes}
          onRow={(n) => ({ onClick: () => setSelected(n), style: { cursor: 'pointer' } })}
          locale={{ emptyText: '暂无节点，请联系管理员开通' }}
          columns={[
            { title: '节点', dataIndex: 'name' },
            { title: '协议', dataIndex: 'type', render: (v: string) => <Tag>{v}</Tag> },
            { title: '地址', render: (_, n: UserNode) => `${n.server}:${n.port}` },
            {
              title: '',
              width: 80,
              render: (_, n: UserNode) => (
                <Button type="link" size="small" onClick={(e) => { e.stopPropagation(); setSelected(n) }}>
                  详情 / 二维码
                </Button>
              ),
            },
          ]}
        />
      </Card>

      <Modal
        title={selected?.name}
        open={!!selected}
        onCancel={() => setSelected(null)}
        footer={<Button onClick={() => setSelected(null)}>关闭</Button>}
        width={560}
      >
        {selected && (
          <Space direction="vertical" size="middle" style={{ width: '100%' }}>
            {selected.link ? (
              <div style={{ textAlign: 'center' }}>
                <div style={{ display: 'inline-block', padding: 12, background: '#fff', borderRadius: 8, border: '1px solid #eee' }}>
                  <QRCodeSVG value={selected.link} size={196} />
                </div>
                <div style={{ marginTop: 8, color: '#999' }}>扫码导入（{selected.type}）</div>
              </div>
            ) : (
              // snell / shadowtls have no standard share-link scheme.
              <Alert
                type="info"
                showIcon
                message={`${selected.type} 没有通用的分享链接格式`}
                description="请用下面的参数在客户端手动添加，或直接使用上方的订阅链接导入。"
              />
            )}
            <Descriptions bordered size="small" column={1}>
              {Object.entries(selected.params).map(([k, v]) => (
                <Descriptions.Item key={k} label={k}>
                  <Typography.Text copyable style={{ wordBreak: 'break-all' }}>{v}</Typography.Text>
                </Descriptions.Item>
              ))}
            </Descriptions>
            <Typography.Paragraph copyable={{ text: selected.link }} code style={{ wordBreak: 'break-all', margin: 0 }}>
              {selected.link}
            </Typography.Paragraph>
          </Space>
        )}
      </Modal>

      <Modal title="修改密码" open={pwdOpen} onOk={doChangePwd} onCancel={() => setPwdOpen(false)} destroyOnClose>
        <Form form={pwdForm} layout="vertical">
          <Form.Item name="old_password" label="当前密码" rules={[{ required: true }]}>
            <Input.Password />
          </Form.Item>
          <Form.Item name="new_password" label="新密码" rules={[{ required: true, min: 6 }]}>
            <Input.Password />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  )
}
