import { useEffect, useState } from 'react'
import { Alert, Button, Card, Descriptions, Empty, Form, Input, List, Modal, Segmented, Space, Tag, Typography, message } from 'antd'
import { QRCodeSVG } from 'qrcode.react'
import { changePassword, errMsg, getMe, getUserNodes, resetSub } from '../../api'
import type { UserNode } from '../../api'
import type { User } from '../../types'
import { copyToClipboard } from '../../util'
import { useAuth } from '../../store'
import { CONTINENT_ORDER, continentOf, type Continent } from '../../continents'
import regionData from '../../assets/regions.json'
import { RegionFlag } from '../../components/RegionFlag'

type RegionInfo = { geo: string; coord: [number, number]; label: string }
const REGIONS = regionData as unknown as Record<string, RegionInfo>

const SUB_STYLES = {
  clash: { background: '#16a34a', borderColor: '#16a34a' },
  shadowrocket: { background: '#7c3aed', borderColor: '#7c3aed' },
  surge: { background: '#0ea5e9', borderColor: '#0ea5e9' },
}

const TYPE_COLORS: Record<string, string> = {
  vless: 'blue', vmess: 'purple', trojan: 'geekblue', shadowsocks: 'green',
  hysteria2: 'cyan', hysteria: 'cyan', tuic: 'orange', anytls: 'magenta',
  snell: 'gold', socks: 'default', mixed: 'volcano',
}

const LOGO_VERSION = '20260727-3'
function ClientLogo({ src, monochrome = false }: { src: string; monochrome?: boolean }) {
  return (
    <img src={`${src}?v=${LOGO_VERSION}`} alt="" style={{ width: '1.4em', height: '1.4em', objectFit: 'contain', verticalAlign: '-0.28em', filter: monochrome ? 'brightness(0) invert(1)' : undefined }} />
  )
}

// Chinese label for an ISO region code (HK → 香港), falls back to the code.
const labelOf = (code: string): string => REGIONS[code]?.label || (code === 'Other' ? '其他' : code)

export default function Dashboard() {
  const setAuth = useAuth((s) => s.setAuth)
  const setUser = useAuth((s) => s.setUser)
  const [user, setLocalUser] = useState<User | null>(null)
  const [subUrl, setSubUrl] = useState('')
  const [nodes, setNodes] = useState<UserNode[]>([])
  const [continentNodes, setContinentNodes] = useState<Partial<Record<Continent, UserNode[]>>>({})
  const [seg, setSeg] = useState<Continent>('亚洲')
  const [selectedNode, setSelectedNode] = useState<UserNode | null>(null)
  const [pwdOpen, setPwdOpen] = useState(false)
  const [pwdForm] = Form.useForm()

  const load = () => {
    getMe().then((d) => { setLocalUser(d.user); setSubUrl(d.subscription_url); setUser(d.user) }).catch((e) => message.error(errMsg(e)))
    getUserNodes().then(setNodes).catch((e) => message.error(errMsg(e)))
  }
  useEffect(load, [])

  useEffect(() => {
    if (nodes.length === 0) return
    const byContinent: Partial<Record<Continent, UserNode[]>> = {}
    for (const n of nodes) {
      const c = continentOf(n.region)
      if (!byContinent[c]) byContinent[c] = []
      byContinent[c].push(n)
    }
    setContinentNodes(byContinent)
  }, [nodes])

  if (!user) return null

  const link = (target: string) => (target ? `${subUrl}?target=${target}` : subUrl)
  const copySub = async (target: string, label: string) => {
    try {
      await copyToClipboard(link(target))
      message.success(`已复制 ${label} 订阅链接`)
    } catch {
      Modal.info({ title: `${label} 订阅链接`, width: 640, content: <Typography.Paragraph copyable code style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{link(target)}</Typography.Paragraph> })
    }
  }
  const doReset = () => Modal.confirm({ title: '重置订阅链接?', content: '旧链接将立即失效，需要在客户端重新导入。', onOk: async () => { const r = await resetSub(); setSubUrl(r.subscription_url); message.success('已重置') } })
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

  // Only show continents that actually have nodes; keep the current segment
  // valid if it becomes empty after a refresh.
  const segOptions = CONTINENT_ORDER
    .map((c) => ({ value: c, count: continentNodes[c]?.length ?? 0 }))
    .filter((o) => o.count > 0)
    .map((o) => ({ label: `${o.value} ${o.count}`, value: o.value }))
  const activeSeg = segOptions.some((o) => o.value === seg) ? seg : (segOptions[0]?.value ?? '其他')

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Card title="订阅链接">
        <div style={{ color: '#888', marginBottom: 12 }}>点击按钮复制对应客户端的订阅链接</div>
        <Space wrap>
          <Button type="primary" icon={<ClientLogo src="/logos/clashmeta.png" />} style={SUB_STYLES.clash} onClick={() => copySub('clash', 'ClashMeta')}>ClashMeta</Button>
          <Button type="primary" icon={<ClientLogo src="/logos/shadowrocket.png" monochrome />} style={SUB_STYLES.shadowrocket} onClick={() => copySub('shadowrocket', 'Shadowrocket')}>Shadowrocket</Button>
          <Button type="primary" icon={<ClientLogo src="/logos/surge.png" monochrome />} style={SUB_STYLES.surge} onClick={() => copySub('surge', 'Surge')}>Surge</Button>
        </Space>
        <div style={{ marginTop: 20 }}>
          <Space>
            <Button onClick={doReset}>重置订阅链接</Button>
            <Button onClick={() => setPwdOpen(true)}>修改密码</Button>
          </Space>
        </div>
      </Card>

      <Card title={`可用节点（${nodes.length}）`}>
        {nodes.length === 0 ? (
          <div style={{ padding: '32px 0', textAlign: 'center', color: '#8c8c8c' }}>暂无节点，请联系管理员开通</div>
        ) : (
          <Space direction="vertical" size="middle" style={{ width: '100%' }}>
            <Segmented block options={segOptions} value={activeSeg} onChange={(v) => setSeg(v as Continent)} />
            {(continentNodes[activeSeg]?.length ?? 0) > 0 ? (
              <List
                itemLayout="horizontal"
                dataSource={continentNodes[activeSeg] ?? []}
                renderItem={(n) => (
                  <List.Item onClick={() => setSelectedNode(n)} style={{ cursor: 'pointer', padding: '10px 2px' }}>
                    <List.Item.Meta
                      avatar={<RegionFlag code={n.region} size={24} />}
                      title={<span style={{ fontWeight: 600 }}>{n.name}</span>}
                      description={
                        <Space size={[4, 4]} wrap>
                          <Tag color={TYPE_COLORS[n.type]}>{n.type}</Tag>
                          {labelOf(n.region || '') && <span style={{ color: '#8c8c8c', fontSize: 12 }}>{labelOf(n.region || '')}</span>}
                          <span style={{ fontFamily: 'monospace', fontSize: 12, color: '#6b7280' }}>{n.server}:{n.port}</span>
                        </Space>
                      }
                    />
                  </List.Item>
                )}
              />
            ) : (
              <Empty description="该大区暂无节点" style={{ padding: '24px 0' }} />
            )}
          </Space>
        )}
      </Card>

      <Modal title={selectedNode?.name} open={!!selectedNode} onCancel={() => setSelectedNode(null)} footer={<Button onClick={() => setSelectedNode(null)}>关闭</Button>} width={560} styles={{ body: { maxHeight: '70vh', overflowY: 'auto' } }}>
        {selectedNode && (
          <Space direction="vertical" size="middle" style={{ width: '100%' }}>
            {selectedNode.link ? (
              <div style={{ textAlign: 'center' }}>
                <div style={{ display: 'inline-block', padding: 12, background: '#fff', borderRadius: 8, border: '1px solid #eee' }}>
                  <QRCodeSVG value={selectedNode.link} size={196} />
                </div>
                <div style={{ marginTop: 8, color: '#999' }}>扫码导入（{selectedNode.type}）</div>
              </div>
            ) : (
              <Alert type="info" showIcon message={`${selectedNode.type} 没有通用的分享链接格式`} description="请用下面的参数在客户端手动添加，或直接使用上方的订阅链接导入。" />
            )}
            <Descriptions bordered size="small" column={1}>
              {Object.entries(selectedNode.params).map(([k, v]) => (
                <Descriptions.Item key={k} label={k}><Typography.Text copyable style={{ wordBreak: 'break-all' }}>{v}</Typography.Text></Descriptions.Item>
              ))}
            </Descriptions>
            <Typography.Paragraph copyable={{ text: selectedNode.link }} code style={{ wordBreak: 'break-all', margin: 0 }}>{selectedNode.link}</Typography.Paragraph>
          </Space>
        )}
      </Modal>

      <Modal title="修改密码" open={pwdOpen} onOk={doChangePwd} onCancel={() => setPwdOpen(false)} destroyOnClose>
        <Form form={pwdForm} layout="vertical">
          <Form.Item name="old_password" label="当前密码" rules={[{ required: true }]}><Input.Password /></Form.Item>
          <Form.Item name="new_password" label="新密码" rules={[{ required: true, min: 6 }]}><Input.Password /></Form.Item>
        </Form>
      </Modal>
    </Space>
  )
}
