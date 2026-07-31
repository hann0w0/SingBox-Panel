import { useEffect, useState } from 'react'
import { Alert, Button, Card, Col, Modal, Row, Space, Statistic, Tag, Upload, message } from 'antd'
import type { UploadFile } from 'antd'
import { CloudDownloadOutlined, DownloadOutlined, InboxOutlined, ReloadOutlined, RocketOutlined, UploadOutlined } from '@ant-design/icons'
import { downloadBackup, errMsg, getMaintenanceInfo, restoreBackup, selfUpdate, type MaintenanceInfo } from '../../api'
import { formatDuration } from '../../util'

export default function Settings() {
  const [info, setInfo] = useState<MaintenanceInfo | null>(null)
  const [loading, setLoading] = useState(false)
  const [updating, setUpdating] = useState(false)
  const [backing, setBacking] = useState(false)
  const [restoring, setRestoring] = useState(false)
  const [restoreFile, setRestoreFile] = useState<UploadFile | null>(null)

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

  const doRestore = () => {
    const file = restoreFile?.originFileObj as File | undefined
    if (!file) {
      message.warning('请先选择备份文件')
      return
    }
    Modal.confirm({
      title: '恢复备份',
      content: (
        <div>
          <p>
            将用 <b>{restoreFile?.name}</b> 覆盖当前所有数据（节点、用户、订阅、被控 Agent 密钥）。
          </p>
          <p style={{ color: '#a61d24', marginBottom: 0 }}>
            覆盖前会自动把现有数据库另存为 .pre-restore 备份以便回滚。
            恢复后面板会重启，你需要重新登录。此操作不可撤销。
          </p>
        </div>
      ),
      okText: '确认恢复',
      okButtonProps: { danger: true },
      cancelText: '取消',
      onOk: async () => {
        setRestoring(true)
        try {
          const r = await restoreBackup(file)
          message.success(r.message)
          setRestoreFile(null)
          if (r.restarting) {
            // The panel exits and systemd restarts it against the restored DB.
            // Sessions are only preserved if the imported jwt_secret was applied;
            // either way, send the admin back to login after a short wait.
            setTimeout(() => {
              localStorage.removeItem('singbox-panel_token')
              localStorage.removeItem('singbox-panel_user')
              location.href = '/login'
            }, 3500)
          } else {
            setRestoring(false)
          }
        } catch (e) {
          message.error(errMsg(e))
          setRestoring(false)
        }
      },
    })
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
    <Row gutter={[16, 16]}>
      <Col xs={24}>
        <Card
          title="面板版本"
          loading={loading}
          extra={<Button size="small" icon={<ReloadOutlined />} onClick={load} disabled={updating}>刷新</Button>}
        >
          {info && (
            <Row gutter={[16, 16]} align="middle">
              <Col xs={12} sm={8} md={6}>
                <Statistic
                  title="当前版本"
                  value={info.current_version}
                  valueStyle={{ fontSize: 22 }}
                />
              </Col>
              <Col xs={12} sm={8} md={6}>
                <Statistic
                  title="最新版本"
                  value={info.latest_version || '—'}
                  valueStyle={{ fontSize: 22, color: info.has_update ? '#d46b08' : undefined }}
                />
              </Col>
              <Col xs={12} sm={8} md={6}>
                <Statistic title="数据库" value={info.db_driver} valueStyle={{ fontSize: 22 }} />
              </Col>
              <Col xs={12} sm={8} md={6}>
                <Statistic
                  title="已运行"
                  value={formatDuration(info.uptime_seconds)}
                  valueStyle={{ fontSize: 22 }}
                />
              </Col>
              <Col xs={24}>
                {!info.update_supported ? (
                  <Alert
                    type="info"
                    showIcon
                    message="此部署不支持面板内更新"
                    description={info.update_reason}
                  />
                ) : info.has_update ? (
                  <Button type="primary" icon={<RocketOutlined />} loading={updating} onClick={doUpdate}>
                    更新到 {info.latest_version}
                  </Button>
                ) : info.latest_version ? (
                  <Tag color="success" style={{ fontSize: 13, padding: '4px 10px' }}>已是最新版本</Tag>
                ) : info.latest_error ? (
                  <span style={{ color: '#8c8c8c' }}>{info.latest_error}</span>
                ) : null}
              </Col>
            </Row>
          )}
        </Card>
      </Col>

      <Col xs={24} lg={12}>
        <Card title="数据备份" style={{ height: '100%' }}>
          <p style={{ color: '#595959', minHeight: 66 }}>
            导出包含全部数据（节点、用户、订阅 token、被控 Agent 密钥）与会话密钥（jwt_secret）的备份。
            在新服务器上恢复此备份并让原域名指向新机，被控 Agent 会自动重连、用户登录也不会失效，无需重装。
          </p>
          <Button
            type="primary"
            icon={backing ? <DownloadOutlined /> : <CloudDownloadOutlined />}
            loading={backing}
            onClick={doBackup}
          >
            下载备份
          </Button>
        </Card>
      </Col>

      <Col xs={24} lg={12}>
        <Card title="恢复备份" style={{ height: '100%' }}>
          <p style={{ color: '#595959', minHeight: 66 }}>
            上传此前下载的备份文件（.tar.gz），覆盖当前数据并重启面板。常用于迁移到新服务器或回滚。
            恢复前会自动把现有数据库另存为 <code>.pre-restore</code> 以便回滚。
          </p>
          <Space direction="vertical" style={{ display: 'flex' }} size={12}>
            <Upload.Dragger
              accept=".gz,.tar.gz"
              maxCount={1}
              multiple={false}
              beforeUpload={() => false /* 不自动上传，交给按钮统一提交 */}
              fileList={restoreFile ? [restoreFile] : []}
              onChange={({ fileList }) => setRestoreFile(fileList[fileList.length - 1] ?? null)}
              onRemove={() => setRestoreFile(null)}
              disabled={restoring}
            >
              <p className="ant-upload-drag-icon" style={{ marginBottom: 4 }}>
                <InboxOutlined />
              </p>
              <p className="ant-upload-text">点击或拖拽备份文件到此处</p>
              <p className="ant-upload-hint" style={{ color: '#8c8c8c' }}>
                仅支持本面板导出的 singbox-panel-backup-*.tar.gz
              </p>
            </Upload.Dragger>
            <Button
              danger
              type="primary"
              icon={<UploadOutlined />}
              loading={restoring}
              disabled={!restoreFile}
              onClick={doRestore}
            >
              恢复并重启
            </Button>
          </Space>
        </Card>
      </Col>
    </Row>
  )
}
