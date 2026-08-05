import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Card, Descriptions, Form, Grid, Input, Modal, Space, Tag, Typography, message } from 'antd'
import * as echarts from 'echarts/core'
import { MapChart, ScatterChart } from 'echarts/charts'
import { GeoComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { EChartsOption } from 'echarts'
import worldGeoUrl from '../../assets/world.json?url'
import { QRCodeSVG } from 'qrcode.react'
import { changePassword, errMsg, getMe, getUserNodes, resetSub } from '../../api'
import type { UserNode } from '../../api'
import type { User } from '../../types'
import { copyToClipboard } from '../../util'
import { useAuth } from '../../store'

echarts.use([MapChart, ScatterChart, GeoComponent, TooltipComponent, CanvasRenderer])

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

// Full ISO-code → { geo country name, [lng,lat] centroid, zh label } table,
// generated from world.json + i18n-iso-countries (scripts/gen-regions.cjs).
// Every country a node can be in is covered, so new regions light up
// automatically without editing any hand-written whitelist.
import regionData from '../../assets/regions.json'
type RegionInfo = { geo: string; coord: [number, number]; label: string }
const REGIONS = regionData as unknown as Record<string, RegionInfo>

const geoOf = (code: string) => REGIONS[code]?.geo
const coordOf = (code: string) => REGIONS[code]?.coord
const labelOf = (code: string) => REGIONS[code]?.label || (code === 'Other' ? '其他' : code)

// Fetch the world GeoJSON once and cache the promise, so re-running the map
// effect (e.g. a mobile/desktop breakpoint toggle) reuses it instead of
// re-downloading ~1 MB every time.
let worldGeoPromise: Promise<any> | null = null
function loadWorldGeo(): Promise<any> {
  if (!worldGeoPromise) {
    worldGeoPromise = fetch(worldGeoUrl).then(r => r.json()).catch((e) => {
      worldGeoPromise = null // allow retry on next mount if the fetch failed
      throw e
    })
  }
  return worldGeoPromise
}

export default function Dashboard() {
  const setAuth = useAuth((s) => s.setAuth)
  const setUser = useAuth((s) => s.setUser)
  const [user, setLocalUser] = useState<User | null>(null)
  const [subUrl, setSubUrl] = useState('')
  const [nodes, setNodes] = useState<UserNode[]>([])
  const [regionNodes, setRegionNodes] = useState<Record<string, UserNode[]>>({})
  const [selectedRegion, setSelectedRegion] = useState<string | null>(null)
  const [selectedNode, setSelectedNode] = useState<UserNode | null>(null)
  const [pwdOpen, setPwdOpen] = useState(false)
  const [pwdForm] = Form.useForm()
  const chartRef = useRef<HTMLDivElement>(null)
  const screens = Grid.useBreakpoint()
  const isMobile = !screens.md

  const load = () => {
    getMe().then((d) => { setLocalUser(d.user); setSubUrl(d.subscription_url); setUser(d.user) }).catch((e) => message.error(errMsg(e)))
    getUserNodes().then(setNodes).catch((e) => message.error(errMsg(e)))
  }
  useEffect(load, [])

  useEffect(() => {
    if (nodes.length === 0) return
    const byRegion: Record<string, UserNode[]> = {}
    for (const n of nodes) {
      const r = n.region || 'Other'
      if (!byRegion[r]) byRegion[r] = []
      byRegion[r].push(n)
    }
    setRegionNodes(byRegion)
  }, [nodes])

  useEffect(() => {
    if (!chartRef.current || Object.keys(regionNodes).length === 0) return
    const chart = echarts.init(chartRef.current)
    // Guard the async map load: if the effect re-runs (breakpoint toggle) or the
    // component unmounts before the fetch resolves, the cleanup disposes `chart`
    // and this flag stops the stale callback from touching a disposed instance.
    let cancelled = false

    loadWorldGeo().then(geoJson => {
      if (cancelled || chart.isDisposed()) return
      echarts.registerMap('world', geoJson)
      const activeRegions = Object.keys(regionNodes).filter(r => r !== 'Other')
      // Group active regions by their mapped country so China (HK+CN) merges.
      const activeCountries = Array.from(new Set(activeRegions.map(r => geoOf(r)).filter(Boolean)))

      // Scatter markers make small regions (Singapore, Hong Kong) visible and give
      // every region a clear, labeled click target regardless of country size.
      const scatter = activeRegions.map(r => {
        const coord = coordOf(r)
        if (!coord) return null
        return { name: r, value: [...coord, regionNodes[r].length] }
      }).filter(Boolean) as { name: string; value: number[] }[]

      const option: EChartsOption = {
        tooltip: {
          trigger: 'item',
          formatter: (p: any) => (p.seriesType === 'scatter' ? `${labelOf(p.name)} · ${p.value[2]} 个节点` : ''),
        },
        geo: {
          map: 'world',
          roam: isMobile ? 'move' : true,
          scaleLimit: { min: 1, max: 8 },
          layoutCenter: ['50%', '50%'],
          layoutSize: isMobile ? '160%' : '120%',
          silent: true,
          itemStyle: { areaColor: '#eef0f4', borderColor: '#fff' },
          regions: activeCountries.map(c => ({ name: c, itemStyle: { areaColor: '#178a3a' } })),
        },
        series: [{
          type: 'scatter',
          coordinateSystem: 'geo',
          symbolSize: (val: number[]) => Math.min(16, 8 + val[2]),
          itemStyle: { color: '#0f7a30', shadowBlur: 6, shadowColor: 'rgba(23,138,58,0.5)' },
          label: {
            show: true,
            position: 'right',
            formatter: (p: any) => labelOf(p.name),
            color: '#1f2430',
            fontSize: 12,
            fontWeight: 'bold',
          },
          emphasis: { scale: 1.3 },
          data: scatter,
        }],
      }
      chart.setOption(option)
      chart.on('click', (params: any) => {
        if (params.seriesType === 'scatter' && regionNodes[params.name]) {
          setSelectedRegion(params.name)
        }
      })
    }).catch(() => { if (!cancelled) message.error('地图加载失败') })

    const handleResize = () => chart.resize()
    window.addEventListener('resize', handleResize)
    return () => { cancelled = true; window.removeEventListener('resize', handleResize); chart.dispose() }
  }, [regionNodes, isMobile])

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

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Card title="订阅链接">
        <div style={{ color: '#888', marginBottom: 12 }}>点击按钮复制对应客户端的订阅链接</div>
        <Space wrap>
          <Button type="primary" icon={<ClientLogo src="/logos/clashmeta.png" />} style={SUB_STYLES.clash} onClick={() => copySub('clash', 'ClashMeta')}>ClashMeta</Button>
          <Button type="primary" icon={<ClientLogo src="/logos/shadowrocket.png" monochrome />} style={SUB_STYLES.shadowrocket} onClick={() => copySub('', 'Shadowrocket')}>Shadowrocket</Button>
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
          <div ref={chartRef} style={{ width: '100%', height: isMobile ? 320 : 480 }} />
        )}
      </Card>

      <Modal
        title={`${labelOf(selectedRegion || '')} 节点`}
        open={!!selectedRegion}
        onCancel={() => setSelectedRegion(null)}
        footer={<Button onClick={() => setSelectedRegion(null)}>关闭</Button>}
        width={720}
        styles={{ body: { maxHeight: '60vh', overflowY: 'auto' } }}
      >
        {selectedRegion && regionNodes[selectedRegion] && (
          <Space direction="vertical" size="middle" style={{ width: '100%' }}>
            {regionNodes[selectedRegion].map((n) => (
              <Card key={`${n.server}:${n.port}:${n.name}`} size="small" hoverable onClick={() => setSelectedNode(n)} style={{ cursor: 'pointer' }}>
                <div style={{ fontWeight: 600, marginBottom: 4 }}>{n.name}</div>
                <Space size={[4, 4]} wrap>
                  <Tag color={TYPE_COLORS[n.type]}>{n.type}</Tag>
                  <span style={{ fontFamily: 'monospace', fontSize: 12, color: '#6b7280' }}>{n.server}:{n.port}</span>
                </Space>
              </Card>
            ))}
          </Space>
        )}
      </Modal>

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
