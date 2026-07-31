import { useEffect, useState } from 'react'
import { Alert, Button, Card, Descriptions, Modal, Space, Tag, message } from 'antd'
import { CloudDownloadOutlined, DownloadOutlined, ReloadOutlined, RocketOutlined } from '@ant-design/icons'
import { downloadBackup, errMsg, getMaintenanceInfo, selfUpdate, type MaintenanceInfo } from '../../api'

export default function Settings() {
  const [info, setInfo] = useState<MaintenanceInfo | null>(null)
  const [loading, setLoading] = useState(false)
  const [updating, setUpdating] = useState(false)
  const [backing, setBacking] = useState(false)

  const load = () => {
    setLoading(true)
    getMaintenanceInfo()
      .then(setInfo)
      .catch((e) => message.error(errMsg(e)))
      .finally(() => setLoading(false))
  }
  useEffect(load, [])

  const doBackup = () => {
    setBacking(true)
    downloadBackup()
      .then(() => message.success('备份已开始下载'))
      .catch((e) => message.error(errMsg(e)))
      .finally(() => setBacking(false))
  }

  const doUpdate = () => {
    const target = info?.latest_version
    Modal.confirm({
      title: '更新面板',
      content: (
        <div>
          <p>
            将从 {info?.current_version} 更新到 <b>{target}</b>。
          </p>
          <p style={{ color: '#a61d24', marginBottom: 0 }}>
            面板会下载新版二进制、校验后替换并自动重启，期间约中断数秒。
            数据、域名、端口、被控 Agent 均不受影响。
          </p>
        </div>
      ),
      okText: '开始更新',
      cancelText: '取消',
      onOk: async () => {
        setUpdating(true)
        try {
          const r = await selfUpdate(target)
          if (!r.updated) {
            message.info(r.message)
            setUpdating(false)
            return
          }
          message.success(r.message)
          // The panel restarts; poll /info until the version flips, then reload.
          waitForRestart(target)
        } catch (e) {
          message.error(errMsg(e))
          setUpdating(false)
        }
      },
    })
  }

  // After a self-update the backend restarts. Poll until it answers with the
  // new version (or times out), keeping the button spinner honest.
  const waitForRestart = (target?: string) => {
    let tries = 0
    const timer = setInterval(async () => {
      tries += 1
      try {
        const fresh = await getMaintenanceInfo()
        if (!target || fresh.current_version.replace(/^v/, '') === target.replace(/^v/, '')) {
          clearInterval(timer)
          setInfo(fresh)
          setUpdating(false)
          message.success('面板已更新并重启完成')
        }
      } catch {
        // still restarting; keep waiting
      }
      if (tries > 30) {
        clearInterval(timer)
        setUpdating(false)
        message.warning('更新可能仍在进行，请稍后刷新页面确认版本')
      }
    }, 2000)
  }

  return (
    <Space direction="vertical" size="large" style={{ display: 'flex', maxWidth: 820 }}>
      <Card
        title="面板版本"
        loading={loading}
        extra={<Button size="small" icon={<ReloadOutlined />} onClick={load} disabled={updating}>刷新</Button>}
      >
        {info && (
          <>
            <Descriptions column={1} size="small" style={{ marginBottom: 16 }}>
              <Descriptions.Item label="当前版本">
                <Tag color="blue">{info.current_version}</Tag>
              </Descriptions.Item>
              <Descriptions.Item label="最新版本">
                {info.latest_version ? (
                  <Tag color={info.has_update ? 'orange' : 'green'}>{info.latest_version}</Tag>
                ) : (
                  <span style={{ color: '#8c8c8c' }}>{info.latest_error || '获取中…'}</span>
                )}
              </Descriptions.Item>
            </Descriptions>

            {!info.update_supported && (
              <Alert
                type="info"
                showIcon
                message="此部署不支持面板内更新"
                description={info.update_reason}
                style={{ marginBottom: 16 }}
              />
            )}

            {info.update_supported && info.has_update && (
              <Button type="primary" icon={<RocketOutlined />} loading={updating} onClick={doUpdate}>
                更新到 {info.latest_version}
              </Button>
            )}
            {info.update_supported && !info.has_update && info.latest_version && (
              <Alert type="success" showIcon message="已是最新版本" />
            )}
          </>
        )}
      </Card>

      <Card title="数据备份">
        <p style={{ color: '#595959' }}>
          导出包含全部数据（节点、用户、订阅 token、被控 Agent 密钥）与会话密钥（jwt_secret）的备份。
          在新服务器上恢复此备份并让原域名指向新机，被控 Agent 会自动重连、用户登录也不会失效，无需重装。
        </p>
        <Button icon={backing ? <DownloadOutlined /> : <CloudDownloadOutlined />} loading={backing} onClick={doBackup}>
          下载备份
        </Button>
      </Card>
    </Space>
  )
}
