import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Input, Modal, Popconfirm, Select, Tooltip, Typography, Upload, message } from '../../../ui'
import type { UploadFile } from '../../../ui'
import { Trash2, Download, Inbox, Link, RotateCw } from 'lucide-react'
import {
  listPanelReleases,
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
  setOneDrivePassphrase,
  selfUpdate,
  startOneDriveAuth,
  syncOneDriveBackup,
  type MaintenanceInfo,
  type OneDriveAuthStart,
  type OneDriveStatus,
} from '../../../api'
import { formatDuration } from '../../../util'
import { useAuth } from '../../../store'
import { updateFailureMessage, updateRestartCompleted } from '../../../maintenanceUpdate'
import { PageHeader, Pills, Status } from '../../../ui/Page'
import { RequestState } from '../../../components/RequestState'
import type { PanelRelease } from '../../../api'
import '../../../styles/settings-user.css'

// Orders vMAJOR.MINOR.PATCH tags; unparsable tags compare equal.
function compareVersions(a: string, b: string): number {
  const parse = (v: string) => v.replace(/^v/, '').split(/[.-]/).slice(0, 3).map((n) => Number(n))
  const pa = parse(a), pb = parse(b)
  if (pa.length < 3 || pb.length < 3 || [...pa, ...pb].some((n) => !Number.isFinite(n))) return 0
  for (let i = 0; i < 3; i++) if (pa[i] !== pb[i]) return pa[i] > pb[i] ? 1 : -1
  return 0
}

type SettingsView = 'version' | 'backup' | 'restore'
const VIEWS = [
  { key: 'version', label: '版本' },
  { key: 'backup', label: '备份' },
  { key: 'restore', label: '恢复' },
]

export default function Settings() {
  const [view, setView] = useState<SettingsView>('version')
  const [info, setInfo] = useState<MaintenanceInfo | null>(null)
  const dockerDeployment = info?.deployment === 'docker'
  const [loading, setLoading] = useState(false)
  const [updating, setUpdating] = useState(false)
  const [releases, setReleases] = useState<PanelRelease[] | null>(null)
  const [releasesLoading, setReleasesLoading] = useState(false)
  const [releasesError, setReleasesError] = useState<string | null>(null)
  const [pickedVersion, setPickedVersion] = useState<string>()
  const [backing, setBacking] = useState(false)
  const [restoring, setRestoring] = useState(false)
  const [restoreFile, setRestoreFile] = useState<UploadFile | null>(null)
  const [restorePassphrase, setRestorePassphrase] = useState('')
  const [cloudPassphrase, setCloudPassphrase] = useState('')
  const [cloudPassphraseSaving, setCloudPassphraseSaving] = useState(false)
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

  const doSaveCloudPassphrase = async () => {
    if ([...cloudPassphrase].length < 12) {
      message.warning('备份口令至少 12 个字符')
      return
    }
    setCloudPassphraseSaving(true)
    try {
      await setOneDrivePassphrase(cloudPassphrase)
      setCloudPassphrase('')
      message.success('备份口令已保存，请另行妥善保管：丢失后无法解密云端备份')
      loadCloud()
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      setCloudPassphraseSaving(false)
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

  const doCloudRestore = (id: string, name: string, encrypted: boolean) => {
    // Modal.confirm content is rendered once, so collect the value outside state.
    let passphrase = ''
    Modal.confirm({
      title: '从 OneDrive 恢复备份',
      content: (
        <>
          <p>将用 <b>{name}</b> 覆盖全部数据，完成后面板重启。</p>
          {encrypted && (
            <Input.Password
              aria-label="备份口令"
              placeholder="备份口令（留空则使用本面板已保存的口令）"
              autoComplete="off"
              onChange={(e) => { passphrase = e.target.value }}
            />
          )}
        </>
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
          const result = await restoreOneDriveBackup(id, passphrase || undefined)
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
      content: <>将用 <b>{restoreFile?.name}</b> 覆盖全部数据，完成后需重新登录。</>,
      okText: '确认恢复',
      okButtonProps: { danger: true },
      cancelText: '取消',
      onOk: async () => {
        if (restoreOperationRef.current) return
        restoreOperationRef.current = true
        setRestoring(true)
        try {
          const r = await restoreBackup(file, restorePassphrase || undefined)
          message.success(r.message)
          setRestoreFile(null)
          setRestorePassphrase('')
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

  const releasesRequest = useRef<{ generation: number; controller: AbortController | null }>({ generation: 0, controller: null })
  const loadReleases = (refresh = false) => {
    releasesRequest.current.controller?.abort()
    const controller = new AbortController()
    const generation = ++releasesRequest.current.generation
    releasesRequest.current.controller = controller
    setReleasesLoading(true)
    setReleasesError(null)
    listPanelReleases(controller.signal, refresh)
      .then((list) => { if (generation === releasesRequest.current.generation) setReleases(list) })
      .catch((e) => { if (generation === releasesRequest.current.generation && !controller.signal.aborted) setReleasesError(errMsg(e)) })
      .finally(() => { if (generation === releasesRequest.current.generation) setReleasesLoading(false) })
  }
  useEffect(() => () => {
    releasesRequest.current.generation++
    releasesRequest.current.controller?.abort()
  }, [])
  useEffect(() => {
    if (view === 'version' && info && !dockerDeployment && releases === null && !releasesLoading) loadReleases()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [view, !!info, dockerDeployment])

  // Installs any published release: newer = update, same = reinstall, older = rollback.
  const installVersion = (target: string) => {
    const current = info?.current_version
    if (!target || !current) return
    const order = compareVersions(target, current)
    const same = target.replace(/^v/, '') === current.replace(/^v/, '')
    // Equal version numbers with a different tag (e.g. a pre-release) are not a
    // safe upgrade either, so they take the rollback path.
    const rollback = !same && order <= 0
    const force = same || rollback
    const title = same ? '重新安装当前版本' : rollback ? `回退到 ${target}` : `更新到 ${target}`
    Modal.confirm({
      title,
      okText: same ? '重新安装' : rollback ? '回退' : '开始更新',
      okButtonProps: rollback ? { danger: true } : undefined,
      cancelText: '取消',
      content: rollback
        ? <><span className="mono">{current}</span> → <b className="mono">{target}</b>。会先自动备份当前数据。</>
        : <><span className="mono">{current}</span> → <b className="mono">{target}</b>，期间面板中断数秒。</>,
      onOk: async () => {
        setUpdating(true)
        try {
          const before = await getMaintenanceInfo()
          if (same && !before.instance_id) throw new Error('当前面板尚不支持确认同版本重装，请先更新面板')
          const r = await selfUpdate(target, force)
          if (!r.updated) {
            message.info(r.message)
            setUpdating(false)
            return
          }
          message.success(r.message)
          if (same && !r.operation_id) throw new Error('无法确认重装结果，请稍后刷新页面检查状态')
          waitForRestart(target, before, r.operation_id)
        } catch (e) {
          message.error(errMsg(e))
          setUpdating(false)
        }
      },
    })
  }
  const doUpdate = (force = false) => {
    const target = force ? info?.current_version : info?.latest_version
    if (target) installVersion(target)
  }

  // A same-version reinstall is complete only after a different panel process
  // is serving the target version. Its brief downtime may fall between polls.
  const waitForRestart = (target: string, before: MaintenanceInfo, operationID?: string) => {
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
        const requestTimeout = setTimeout(() => controller.abort(), 5000)
        let complete = false
        try {
          const fresh = await getMaintenanceInfo(controller.signal)
          if (generation !== restartGeneration.current) return
          const failure = updateFailureMessage(fresh, operationID)
          if (failure) {
            complete = true
            setInfo(fresh)
            setUpdating(false)
            message.error(failure)
          } else if (updateRestartCompleted(before, fresh, target, operationID)) {
            complete = true
            setInfo(fresh)
            setUpdating(false)
            message.success('面板已更新并重启完成')
            location.reload()
          }
        } catch {
          // still restarting; schedule another attempt after this one finishes
        } finally {
          clearTimeout(requestTimeout)
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

  const refreshAll = () => { load(true); void loadCloud() }
  const cloudError = cloud?.connected ? (cloud.last_error || cloud.cloud_error) : undefined
  const authLink = authPending ? (authPending.verification_uri_complete || authPending.verification_uri) : ''

  const versionView = (
    <RequestState loading={loading && !info} hasData={!!info}>
      {info && (
        <div className="ui-card settings-card">
          <dl className="ui-kv">
            <div><dt>当前版本</dt><dd className="mono">{info.current_version}</dd></div>
            {dockerDeployment && <div><dt>部署方式</dt><dd>Docker</dd></div>}
            <div>
              <dt>最新版本</dt>
              <dd className="settings-version-latest">
                <span className="mono">{info.latest_version || '—'}</span>
                {info.has_update && <Status tone="warn">可更新</Status>}
                {!info.has_update && info.latest_version && <Status tone="ok">已是最新</Status>}
              </dd>
            </div>
            <div><dt>数据库</dt><dd className="mono">{info.db_driver}</dd></div>
            <div><dt>已运行</dt><dd>{formatDuration(info.uptime_seconds)}</dd></div>
          </dl>
          {(() => {
            const note = !info.has_update && !info.latest_version ? info.latest_error : undefined
            const showUpdate = !!info.has_update && !dockerDeployment
            if (!note && !showUpdate) return null
            return (
              <div className="settings-card-foot">
                {note && <span className="settings-note">{note}</span>}
                {showUpdate && (
                  <div className="settings-actions">
                    {showUpdate && <Button type="primary" loading={updating} disabled={!info.update_supported} title={info.update_supported ? undefined : (info.update_reason || "当前环境不支持在线更新")} onClick={() => doUpdate()}>更新到 {info.latest_version}</Button>}
                  </div>
                )}
              </div>
            )
          })()}
          {dockerDeployment ? (
            <div className="settings-card-foot">
              <Alert type="info" showIcon message="通过 Docker 更新面板" description={info.update_reason || '更新镜像并重建容器即可升级。请保留数据卷，并在升级前下载备份。'} />
            </div>
          ) : (
            <div className="settings-card-foot settings-release-picker">
              <span className="settings-release-label">安装指定版本</span>
              <div className="settings-actions">
                <Select
                  className="settings-release-select"
                  aria-label="选择版本"
                  placeholder={releasesError ? '获取失败' : '选择版本'}
                  loading={releasesLoading}
                  value={pickedVersion}
                  onChange={setPickedVersion}
                  onDropdownVisibleChange={(open) => { if (open && releasesError) loadReleases(true) }}
                  popupMatchSelectWidth={false}
                  options={(releases ?? []).map((r) => ({
                    value: r.version,
                    label: <span className="settings-release-option">
                      <span className="mono">{r.version}</span>
                      {r.current && <span className="ui-tag">当前</span>}
                      {r.prerelease && <span className="ui-tag">预发布</span>}
                      {r.published_at && <span className="faint">{new Date(r.published_at).toLocaleDateString('zh-CN')}</span>}
                    </span>,
                  }))}
                />
                <Button disabled={!pickedVersion || !info.update_supported} title={info.update_supported ? undefined : (info.update_reason || '当前环境不支持在线更新')} loading={updating} onClick={() => pickedVersion && installVersion(pickedVersion)}>
                  {pickedVersion && info.current_version && pickedVersion.replace(/^v/, '') !== info.current_version.replace(/^v/, '') && compareVersions(pickedVersion, info.current_version) <= 0 ? '回退' : '安装'}
                </Button>
                <Button
                  loading={updating}
                  disabled={!info.update_supported || !info.reinstall_supported}
                  title={info.update_supported ? undefined : (info.update_reason || '当前环境不支持在线更新')}
                  onClick={() => doUpdate(true)}
                >
                  重新安装
                </Button>
              </div>
            </div>
          )}
        </div>
      )}
    </RequestState>
  )

  const backupView = (
    <>
      <div className="ui-card settings-card settings-rows">
        <div className="settings-row">
          <div className="settings-row-main">
            <b>本地备份</b>
            <span className="settings-row-meta mono">.tar.gz</span>
          </div>
          <div className="settings-actions">
            <Button icon={<Download />} loading={backing} onClick={doBackup}>下载</Button>
          </div>
        </div>
        <div className="settings-row">
          <div className="settings-row-main">
            <b>OneDrive</b>
            {cloud?.connected ? <Status tone="ok">已连接</Status> : <Status>未连接</Status>}
            {cloud?.connected && (cloud.encrypted ? <Status tone="ok">已加密</Status> : <Status tone="warn">未设置口令</Status>)}
            {cloud?.connected && cloud.last_sync_at && (
              <span className="settings-row-meta">
                {new Date(cloud.last_sync_at).toLocaleString()}
                {cloud.last_backup_name ? <> · <span className="mono">{cloud.last_backup_name}</span></> : null}
              </span>
            )}
          </div>
          <div className="settings-actions">
            <Button type={cloud?.connected ? 'default' : 'primary'} onClick={doConnectCloud} loading={cloudLoading}>
              {cloud?.connected ? '重新连接' : '连接'}
            </Button>
            {cloud?.connected && (
              <Button type="primary" loading={cloudSyncing} disabled={!cloud.encrypted} title={cloud.encrypted ? undefined : '请先设置备份口令'} onClick={doCloudSync}>立即同步</Button>
            )}
          </div>
        </div>
        {cloud?.connected && (
          <div className="settings-row">
            <div className="settings-row-main">
              <b>备份口令</b>
              <span className="settings-row-meta">
                {cloud.encrypted ? '云端备份已加密；更换口令后，旧备份仍需用旧口令恢复' : '备份含全部节点密钥和会话密钥，设置口令后才会上传'}
              </span>
            </div>
            <div className="settings-actions">
              <Input.Password
                aria-label="新的备份口令"
                placeholder={cloud.encrypted ? '新口令（至少 12 位）' : '设置口令（至少 12 位）'}
                autoComplete="new-password"
                value={cloudPassphrase}
                onChange={(e) => setCloudPassphrase(e.target.value)}
                onPressEnter={doSaveCloudPassphrase}
              />
              <Button loading={cloudPassphraseSaving} disabled={!cloudPassphrase} onClick={doSaveCloudPassphrase}>保存</Button>
            </div>
          </div>
        )}
        {authPending && (
          <div className="settings-row settings-auth">
            <div className="settings-row-main">
              <b>验证码</b>
              <Typography.Text className="settings-auth-code mono" copyable={{ text: authPending.user_code }}>{authPending.user_code}</Typography.Text>
            </div>
            <div className="settings-actions">
              <Button icon={<Link />} href={authLink} target="_blank" rel="noreferrer">打开授权页面</Button>
            </div>
          </div>
        )}
      </div>

      {cloud?.connected && !cloud.encrypted && (
        <Alert
          className="settings-alert"
          type="warning"
          showIcon
          message="OneDrive 同步已暂停"
          description="备份包含 jwt_secret 和所有节点密钥。为避免明文存放在云端，请先设置备份口令。"
        />
      )}
      {cloud?.files?.some((file) => !file.encrypted) && (
        <Alert
          className="settings-alert"
          type="warning"
          showIcon
          message="云端存在旧版明文备份"
          description="下一次加密同步成功后会自动删除它；也可以现在手动删除。"
        />
      )}

      {cloudError && <Alert className="settings-alert" type="error" showIcon message={cloudError} />}

      {cloud?.connected && (
        <section className="settings-cloud">
          <header className="ui-section-head">
            <h2>云端备份{cloud.files?.length ? <span className="settings-count">{cloud.files.length}</span> : null}</h2>
            <div className="ui-section-actions">
              <Tooltip title="刷新">
                <Button type="text" icon={<RotateCw />} aria-label="刷新云端备份" onClick={loadCloud} loading={cloudLoading} />
              </Tooltip>
            </div>
          </header>
          <div className="ui-card settings-files">
            {(cloud.files ?? []).length === 0 ? (
              <div className="ui-empty">暂无云端备份</div>
            ) : (cloud.files ?? []).map((file) => (
              <div className="settings-file" key={file.id}>
                <div className="settings-file-main">
                  <span className="settings-file-name mono">{file.name}</span>
                  {!file.encrypted && <Status tone="warn">明文</Status>}
                  <span className="settings-row-meta">{formatBackupSize(file.size)} · {new Date(file.lastModifiedDateTime).toLocaleString()}</span>
                </div>
                <div className="ui-row-actions">
                  <Button type="text" size="small" loading={cloudRestoring} disabled={restoring} onClick={() => doCloudRestore(file.id, file.name, !!file.encrypted)}>恢复</Button>
                  <Tooltip title="下载">
                    <Button type="text" size="small" icon={<Download />} aria-label={`下载 ${file.name}`} onClick={() => doCloudDownload(file.id, file.name)} />
                  </Tooltip>
                  <Popconfirm
                    title="删除这份云端备份？"
                    okText="删除"
                    cancelText="取消"
                    okButtonProps={{ danger: true }}
                    onConfirm={() => doCloudDelete(file.id)}
                  >
                    <Button type="text" size="small" icon={<Trash2 />} aria-label={`删除 ${file.name}`} />
                  </Popconfirm>
                </div>
              </div>
            ))}
          </div>
        </section>
      )}
    </>
  )

  const restoreView = (
    <div className="ui-card settings-card settings-restore">
      <Upload.Dragger
        className="settings-dragger"
        accept=".gz,.tar.gz,.enc"
        maxCount={1}
        multiple={false}
        beforeUpload={() => false /* 不自动上传，交给按钮统一提交 */}
        fileList={restoreFile ? [restoreFile] : []}
        onChange={({ fileList }) => setRestoreFile(fileList[fileList.length - 1] ?? null)}
        onRemove={() => setRestoreFile(null)}
        disabled={restoring || cloudRestoring}
      >
        <p className="ui-upload-drag-icon"><Inbox /></p>
        <p className="ui-upload-text">点击或拖拽备份文件</p>
        <p className="ui-upload-hint mono">.tar.gz / .tar.gz.enc</p>
      </Upload.Dragger>
      <Input.Password
        className="settings-restore-passphrase"
        aria-label="备份口令"
        placeholder="备份口令（仅加密备份需要）"
        autoComplete="off"
        value={restorePassphrase}
        onChange={(e) => setRestorePassphrase(e.target.value)}
        disabled={restoring || cloudRestoring}
      />
      <div className="settings-card-foot">
        <span className="settings-note">将覆盖全部数据并重启面板</span>
        <div className="settings-actions">
          <Button danger type="primary" loading={restoring} disabled={!restoreFile || cloudRestoring} onClick={doRestore}>
            恢复并重启
          </Button>
        </div>
      </div>
    </div>
  )

  return (
    <div className="settings-page">
      <PageHeader
        title="设置"
        actions={<Button icon={<RotateCw />} loading={loading} onClick={refreshAll} disabled={updating}>刷新</Button>}
      />
      <Pills label="设置分类" items={VIEWS} active={view} onChange={(key) => setView(key as SettingsView)} />
      <div className="settings-view">
        {view === 'version' ? versionView : view === 'backup' ? (
          <RequestState loading={cloudLoading && !cloud} hasData={!!cloud}>{backupView}</RequestState>
        ) : restoreView}
      </div>
    </div>
  )
}
