import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Card, Col, Divider, List, Modal, Popconfirm, Row, Space, Statistic, Tag, Typography, Upload, message } from 'antd'
import type { UploadFile } from 'antd'
import { CloudDownloadOutlined, CloudOutlined, DeleteOutlined, DownloadOutlined, InboxOutlined, LinkOutlined, ReloadOutlined, RocketOutlined, UploadOutlined } from '@ant-design/icons'
import {
  deleteOneDriveBackup,
  downloadBackup,
  downloadOneDriveBackup,
  errMsg,
  getMaintenanceInfo,
  getOneDriveStatus,
  pollOneDriveAuth,
  isCanceledRequest,
  restoreBackup,
  restoreOneDriveBackup,
  selfUpdate,
  startOneDriveAuth,
  syncOneDriveBackup,
  type MaintenanceInfo,
  type OneDriveAuthStart,
  type OneDriveStatus,
} from '../../../api'
import { formatDuration } from '../../../util'
import { useAuth } from '../../../store'

export default function Settings() {
  const [info, setInfo] = useState<MaintenanceInfo | null>(null)
  const [loading, setLoading] = useState(false)
  const [updating, setUpdating] = useState(false)
  const [backing, setBacking] = useState(false)
  const [restoring, setRestoring] = useState(false)
  const [restoreFile, setRestoreFile] = useState<UploadFile | null>(null)
  const [cloud, setCloud] = useState<OneDriveStatus | null>(null)
  const [cloudLoading, setCloudLoading] = useState(false)
  const [cloudSyncing, setCloudSyncing] = useState(false)
  const [cloudRestoring, setCloudRestoring] = useState(false)
  const [authPending, setAuthPending] = useState<OneDriveAuthStart | null>(null)
  const authTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const authAbort = useRef<AbortController | null>(null)
  const authGeneration = useRef(0)
  const restartTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const restartAbort = useRef<AbortController | null>(null)
  const restartGeneration = useRef(0)
  const redirectTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const infoLoadRef = useRef<{ generation: number; controller: AbortController | null }>({ generation: 0, controller: null })
  const cloudLoadRef = useRef<{ generation: number; controller: AbortController | null }>({ generation: 0, controller: null })
  const restoreOperationRef = useRef(false)

  const load = (refresh = false) => {
    infoLoadRef.current.controller?.abort()
    const controller = new AbortController()
    const generation = ++infoLoadRef.current.generation
    infoLoadRef.current.controller = controller
    setLoading(true)
    getMaintenanceInfo(controller.signal, refresh)
      .then((next) => {
        if (generation === infoLoadRef.current.generation) setInfo(next)
      })
      .catch((e) => {
        if (generation === infoLoadRef.current.generation && !isCanceledRequest(e)) message.error(errMsg(e))
      })
      .finally(() => {
        if (generation === infoLoadRef.current.generation) {
          infoLoadRef.current.controller = null
          setLoading(false)
        }
      })
  }

  const loadCloud = async () => {
    cloudLoadRef.current.controller?.abort()
    const controller = new AbortController()
    const generation = ++cloudLoadRef.current.generation
    cloudLoadRef.current.controller = controller
    setCloudLoading(true)
    try {
      const status = await getOneDriveStatus(controller.signal)
      if (generation !== cloudLoadRef.current.generation) return
      setCloud(status)
    } catch (e) {
      if (generation === cloudLoadRef.current.generation && !isCanceledRequest(e)) message.error(errMsg(e))
    } finally {
      if (generation === cloudLoadRef.current.generation) {
        cloudLoadRef.current.controller = null
        setCloudLoading(false)
      }
    }
  }

  useEffect(() => {
    load()
    loadCloud()
    return () => {
      authGeneration.current += 1
      if (authTimer.current) clearTimeout(authTimer.current)
      authAbort.current?.abort()
      restartGeneration.current += 1
      if (restartTimer.current) clearTimeout(restartTimer.current)
      restartAbort.current?.abort()
      if (redirectTimer.current) clearTimeout(redirectTimer.current)
      infoLoadRef.current.generation += 1
      infoLoadRef.current.controller?.abort()
      cloudLoadRef.current.generation += 1
      cloudLoadRef.current.controller?.abort()
    }
  }, [])

  const doBackup = () => {
    setBacking(true)
    downloadBackup()
      .then(() => message.success('备份已开始下载'))
      .catch((e) => message.error(errMsg(e)))
      .finally(() => setBacking(false))
  }

  const pollCloudAuth = (sessionID: string, delaySeconds: number, generation: number) => {
    if (generation !== authGeneration.current) return
    authTimer.current = setTimeout(async () => {
      if (generation !== authGeneration.current) return
      const controller = new AbortController()
      authAbort.current = controller
      try {
        const result = await pollOneDriveAuth(sessionID, controller.signal)
        if (generation !== authGeneration.current) return
        if (result.status === 'connected') {
          setAuthPending(null)
          message.success('OneDrive 已连接')
          void loadCloud()
          return
        }
        pollCloudAuth(sessionID, result.interval || delaySeconds, generation)
      } catch (e) {
        if (controller.signal.aborted || generation !== authGeneration.current) return
        setAuthPending(null)
        message.error(errMsg(e))
      } finally {
        if (authAbort.current === controller) authAbort.current = null
      }
    }, Math.max(delaySeconds, 3) * 1000)
  }

  const doConnectCloud = async () => {
    authGeneration.current += 1
    const generation = authGeneration.current
    if (authTimer.current) clearTimeout(authTimer.current)
    authTimer.current = null
    authAbort.current?.abort()
    const controller = new AbortController()
    authAbort.current = controller
    setCloudLoading(true)
    try {
      const auth = await startOneDriveAuth(controller.signal)
      if (generation !== authGeneration.current) return
      setAuthPending(auth)
      const target = auth.verification_uri_complete || auth.verification_uri
      window.open(target, '_blank', 'noopener,noreferrer')
      pollCloudAuth(auth.session_id, auth.interval || 5, generation)
    } catch (e) {
      if (controller.signal.aborted || generation !== authGeneration.current) return
      message.error(errMsg(e))
    } finally {
      if (authAbort.current === controller) authAbort.current = null
      if (generation === authGeneration.current) setCloudLoading(false)
    }
  }

  const doCloudSync = async () => {
    setCloudSyncing(true)
    try {
      const result = await syncOneDriveBackup()
      message.success(result.message)
      loadCloud()
    } catch (e) {
      message.error(errMsg(e))
      loadCloud()
    } finally {
      setCloudSyncing(false)
    }
  }

  const doCloudDownload = async (id: string, name: string) => {
    try {
      await downloadOneDriveBackup(id, name)
    } catch (e) {
      message.error(errMsg(e))
    }
  }

  const doCloudDelete = async (id: string) => {
    try {
      await deleteOneDriveBackup(id)
      message.success('云端备份已删除')
      loadCloud()
    } catch (e) {
      message.error(errMsg(e))
    }
  }

  const doCloudRestore = (id: string, name: string) => {
    Modal.confirm({
      title: '从 OneDrive 恢复备份',
      content: (
        <div>
          <p>服务器将直接从 OneDrive 读取 <b>{name}</b>，覆盖当前节点、用户、订阅和被控 Agent 数据。</p>
          <p style={{ color: '#a61d24', marginBottom: 0 }}>
            恢复前会自动保留当前数据库快照，恢复完成后面板会重启。请确认备份来源可信。
          </p>
        </div>
      ),
      okText: '确认恢复',
      okButtonProps: { danger: true },
      cancelText: '取消',
      onOk: async () => {
        if (restoreOperationRef.current) return
        restoreOperationRef.current = true
        setCloudRestoring(true)
        let restarting = false
        try {
          const result = await restoreOneDriveBackup(id)
          restarting = result.restarting
          message.success(result.message)
          if (result.restarting) {
            if (redirectTimer.current) clearTimeout(redirectTimer.current)
            redirectTimer.current = setTimeout(() => {
              useAuth.getState().logout()
              location.href = '/login'
            }, 3500)
          }
        } catch (e) {
          message.error(errMsg(e))
        } finally {
          // A successful restore is intentionally terminal for this page. Keep
          // every restore control disabled until the process restarts and the
          // redirect runs, preventing a second restore against a changing DB.
          if (!restarting) {
            restoreOperationRef.current = false
            setCloudRestoring(false)
          }
        }
      },
    })
  }

  const formatBackupSize = (size: number) => {
    if (size < 1024 * 1024) return `${Math.max(1, Math.round(size / 1024))} KB`
    return `${(size / 1024 / 1024).toFixed(1)} MB`
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
        if (restoreOperationRef.current) return
        restoreOperationRef.current = true
        setRestoring(true)
        try {
          const r = await restoreBackup(file)
          message.success(r.message)
          setRestoreFile(null)
          if (r.restarting) {
            // The panel exits and systemd restarts it against the restored DB.
            // Sessions are only preserved if the imported jwt_secret was applied;
            // either way, send the admin back to login after a short wait.
            if (redirectTimer.current) clearTimeout(redirectTimer.current)
            redirectTimer.current = setTimeout(() => {
              useAuth.getState().logout()
              location.href = '/login'
            }, 3500)
          } else {
            restoreOperationRef.current = false
            setRestoring(false)
          }
        } catch (e) {
          message.error(errMsg(e))
          restoreOperationRef.current = false
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
            面板会校验并一起切换后端、前端和 Agent 包，失败时自动回滚，期间约中断数秒。
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
    if (restartTimer.current) clearTimeout(restartTimer.current)
    restartTimer.current = null
    restartAbort.current?.abort()
    restartAbort.current = null
    const generation = ++restartGeneration.current
    let tries = 0
    const poll = () => {
      if (generation !== restartGeneration.current) return
      restartTimer.current = setTimeout(async () => {
        restartTimer.current = null
        if (generation !== restartGeneration.current) return
        tries += 1
        const controller = new AbortController()
        restartAbort.current = controller
        let complete = false
        try {
          const fresh = await getMaintenanceInfo(controller.signal)
          if (generation !== restartGeneration.current) return
          if (!target || fresh.current_version.replace(/^v/, '') === target.replace(/^v/, '')) {
            complete = true
            setInfo(fresh)
            setUpdating(false)
            message.success('面板已更新并重启完成')
          }
        } catch {
          // still restarting; schedule another attempt after this one finishes
        } finally {
          if (restartAbort.current === controller) restartAbort.current = null
        }
        if (generation !== restartGeneration.current || complete) return
        if (tries >= 30) {
          setUpdating(false)
          message.warning('更新可能仍在进行，请稍后刷新页面确认版本')
          return
        }
        poll()
      }, 2000)
    }
    poll()
  }

  return (
    <Row gutter={[16, 16]}>
      <Col xs={24}>
        <Card
          title="面板版本"
          loading={loading}
          extra={<Button size="small" icon={<ReloadOutlined />} loading={loading} onClick={() => { load(true); loadCloud() }} disabled={updating}>刷新</Button>}
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
        <Card title="数据备份" style={{ height: '100%' }} loading={cloudLoading && !cloud}>
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

          <Divider style={{ margin: '24px 0 18px' }} />
          <Space align="center" size={10}>
            <Typography.Text strong>OneDrive 云端同步</Typography.Text>
            {cloud?.connected ? <Tag color="success">已连接</Tag> : <Tag>未连接</Tag>}
          </Space>
          <Space wrap size={[10, 10]} style={{ display: 'flex', marginTop: 14 }}>
            <Button type="primary" icon={<CloudOutlined />} onClick={doConnectCloud} loading={cloudLoading}>
              {cloud?.connected ? '重新连接 OneDrive' : '连接 OneDrive'}
            </Button>
            {cloud?.connected && (
              <>
                <Button type="primary" icon={<CloudOutlined />} loading={cloudSyncing} onClick={doCloudSync}>
                  立即同步
                </Button>
                <Button icon={<ReloadOutlined />} onClick={loadCloud} loading={cloudLoading}>
                  刷新列表
                </Button>
              </>
            )}
          </Space>

          {authPending && (
            <Alert
              type="warning"
              showIcon
              style={{ marginTop: 16 }}
              message="请完成 OneDrive 授权"
              description={(
                <Space direction="vertical" size={4}>
                  <span>{authPending.message || '打开授权页面并输入下面的代码。'}</span>
                  <Typography.Text copyable={{ text: authPending.user_code }} strong>
                    验证码：{authPending.user_code}
                  </Typography.Text>
                  <Button
                    type="link"
                    icon={<LinkOutlined />}
                    href={authPending.verification_uri_complete || authPending.verification_uri}
                    target="_blank"
                    rel="noreferrer"
                    style={{ padding: 0, width: 'fit-content' }}
                  >
                    打开 Microsoft 授权页面
                  </Button>
                </Space>
              )}
            />
          )}

          {cloud?.connected && (
            <>
              {(cloud.last_error || cloud.cloud_error) && (
                <Alert
                  type="error"
                  showIcon
                  message={cloud.last_error || cloud.cloud_error}
                  style={{ marginTop: 16 }}
                />
              )}
              {cloud.last_sync_at && (
                <Typography.Text type="secondary" style={{ display: 'block', marginTop: 14 }}>
                  最近同步：{new Date(cloud.last_sync_at).toLocaleString()}
                  {cloud.last_backup_name ? ` · ${cloud.last_backup_name}` : ''}
                </Typography.Text>
              )}
              <List
                size="small"
                bordered
                style={{ marginTop: 14, maxHeight: 280, overflowY: 'auto' }}
                locale={{ emptyText: '暂无云端备份，点击“立即同步”创建第一份备份' }}
                dataSource={cloud.files ?? []}
                renderItem={(file) => (
                  <List.Item
                    actions={[
                      <Button key="restore" type="link" icon={<UploadOutlined />} loading={cloudRestoring} disabled={restoring} onClick={() => doCloudRestore(file.id, file.name)}>
                        恢复
                      </Button>,
                      <Button key="download" type="link" icon={<DownloadOutlined />} onClick={() => doCloudDownload(file.id, file.name)}>
                        下载
                      </Button>,
                      <Popconfirm
                        key="delete"
                        title="删除这份云端备份？"
                        description="删除后无法从 OneDrive 恢复。"
                        okText="删除"
                        cancelText="取消"
                        okButtonProps={{ danger: true }}
                        onConfirm={() => doCloudDelete(file.id)}
                      >
                        <Button danger type="link" icon={<DeleteOutlined />}>删除</Button>
                      </Popconfirm>,
                    ]}
                  >
                    <List.Item.Meta
                      title={file.name}
                      description={`${formatBackupSize(file.size)} · ${new Date(file.lastModifiedDateTime).toLocaleString()}`}
                    />
                  </List.Item>
                )}
              />
            </>
          )}
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
              disabled={restoring || cloudRestoring}
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
              disabled={!restoreFile || cloudRestoring}
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
