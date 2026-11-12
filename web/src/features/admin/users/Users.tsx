import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Button, Table } from '../../../ui'
import type { ColumnsType } from '../../../ui'
import { ChevronRight, Plus } from 'lucide-react'
import { errMsg, listCustomNodes, listServers, listUsers } from '../../../api'
import type { CustomNode } from '../../../api'
import type { Server, User } from '../../../types'
import { formatBytes, relativeTime } from '../../../util'
import { RequestState } from '../../../components/RequestState'
import { PageToolbar } from '../../../ui/Page'
import { UserAccountDialog } from './UserDialogs'
import '../../../styles/users.css'

export default function Users() {
  const navigate = useNavigate()
  const [users, setUsers] = useState<User[]>([])
  const [nodes, setNodes] = useState<CustomNode[]>([])
  const [servers, setServers] = useState<Server[]>([])
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [, setNodesLoading] = useState(false)
  const [, setNodesError] = useState<string | null>(null)
  const [open, setOpen] = useState(false)
  const userLoadRef = useRef<{ generation: number; controller: AbortController | null }>({ generation: 0, controller: null })
  const nodeLoadRef = useRef<{ generation: number; controller: AbortController | null }>({ generation: 0, controller: null })
  const serverNames = (u: User) => (u.server_ids ?? []).map((id) => servers.find((s) => s.id === id)?.name ?? `#${id}`)

  const load = () => {
    userLoadRef.current.controller?.abort()
    const controller = new AbortController()
    const generation = ++userLoadRef.current.generation
    userLoadRef.current.controller = controller
    setLoading(true)
    setLoadError(null)
    listUsers(controller.signal)
      .then((next) => {
        if (generation === userLoadRef.current.generation) setUsers(next)
      })
      .catch((e) => {
        if (generation === userLoadRef.current.generation && !controller.signal.aborted) setLoadError(errMsg(e))
      })
      .finally(() => {
        if (generation === userLoadRef.current.generation) {
          userLoadRef.current.controller = null
          setLoading(false)
        }
      })
  }
  useEffect(() => {
    load()
    return () => {
      userLoadRef.current.generation++
      userLoadRef.current.controller?.abort()
    }
  }, [])

  const loadNodes = () => {
    nodeLoadRef.current.controller?.abort()
    const controller = new AbortController()
    const generation = ++nodeLoadRef.current.generation
    nodeLoadRef.current.controller = controller
    setNodesLoading(true)
    setNodesError(null)
    listCustomNodes(controller.signal)
      .then((next) => {
        if (generation === nodeLoadRef.current.generation) setNodes(next)
      })
      .catch((e) => {
        if (generation === nodeLoadRef.current.generation && !controller.signal.aborted) setNodesError(errMsg(e))
      })
      .finally(() => {
        if (generation === nodeLoadRef.current.generation) {
          nodeLoadRef.current.controller = null
          setNodesLoading(false)
        }
      })
  }
  useEffect(() => {
    loadNodes()
    return () => {
      nodeLoadRef.current.generation++
      nodeLoadRef.current.controller?.abort()
    }
  }, [])
  useEffect(() => {
    const controller = new AbortController()
    listServers(controller.signal)
      .then((next) => {
        if (!controller.signal.aborted) setServers(next)
      })
      .catch(() => {
        // Names are decoration for the 服务器 column; counts still render.
      })
    return () => controller.abort()
  }, [])

  const addUserButton = <Button type="primary" icon={<Plus />} onClick={() => setOpen(true)}>新增用户</Button>

  // A custom node is visible to a user when it targets everyone (minus
  // exclusions) or lists the user explicitly. Mirrors model.CustomNode.HasUser.
  const nodeVisible = (node: CustomNode, userID: number) =>
    node.enabled && (node.all_users ? !node.excluded_user_ids?.includes(userID) : node.user_ids?.includes(userID))
  const externalGroups = (u: User) => {
    const counts = new Map<string, number>()
    for (const node of nodes) {
      if (!nodeVisible(node, u.id)) continue
      const key = node.group || '未分组'
      counts.set(key, (counts.get(key) ?? 0) + 1)
    }
    return [...counts.entries()].sort((a, b) => b[1] - a[1])
  }
  const lastActive = (u: User) => {
    const login = u.last_login_at ? new Date(u.last_login_at).getTime() : 0
    const sub = u.last_sub_at ? new Date(u.last_sub_at).getTime() : 0
    if (!login && !sub) return null
    return sub >= login
      ? { at: u.last_sub_at!, what: `更新订阅${u.last_sub_client ? ` · ${u.last_sub_client}` : ''}`, ip: u.last_sub_ip }
      : { at: u.last_login_at!, what: '登录面板', ip: u.last_login_ip }
  }

  const columns: ColumnsType<User> = [
    {
      title: '用户',
      key: 'email',
      render: (_, u) => (
        <span className="um-user">
          <Link className="um-user-name um-user-link" to={`/admin/users/${u.id}`} aria-label={`管理 ${u.email}`} onClick={(event) => event.stopPropagation()}>{u.email}</Link>
          {u.role === 'admin' ? <span className="ui-tag">管理员</span> : null}
        </span>
      ),
    },
    {
      title: '节点',
      key: 'nodes',
      responsive: ['sm'],
      render: (_, u) => {
        // Panel nodes by server, external nodes by group; no per-node detail.
        const chips = [
          ...serverNames(u).map((name) => ({ key: `s-${name}`, label: name, external: false })),
          ...externalGroups(u).map(([group]) => ({ key: `g-${group}`, label: group, external: true })),
        ]
        if (!chips.length) return <span className="faint">未分配</span>
        const shown = chips.slice(0, 3)
        return (
          <span className="um-chips" title={chips.map((c) => c.label).join('、')}>
            {shown.map((c) => <span key={c.key} className={`ui-tag${c.external ? ' um-chip-ext' : ''}`}>{c.label}</span>)}
            {chips.length > shown.length && <span className="faint">+{chips.length - shown.length}</span>}
          </span>
        )
      },
    },
    {
      title: '近 30 天',
      key: 'traffic',
      width: 120,
      responsive: ['md'],
      render: (_, u) => (u.traffic_upload_30d ?? 0) + (u.traffic_download_30d ?? 0) > 0 ? (
        <span className="mono um-stack">
          <span>↓ {formatBytes(u.traffic_download_30d ?? 0)}</span>
          <small>↑ {formatBytes(u.traffic_upload_30d ?? 0)}</small>
        </span>
      ) : <span className="faint">—</span>,
    },
    {
      title: '最近活动',
      key: 'active',
      width: 150,
      responsive: ['lg'],
      render: (_, u) => {
        const active = lastActive(u)
        if (!active) return <span className="faint">—</span>
        return (
          <span className="um-stack" title={`${new Date(active.at).toLocaleString('zh-CN')}${active.ip ? ` · ${active.ip}` : ''}`}>
            <span>{relativeTime(active.at)}</span>
            <small>{active.what}</small>
          </span>
        )
      },
    },
    {
      title: <span className="sr-only">查看详情</span>,
      key: 'actions',
      width: 44,
      align: 'right',
      render: () => <ChevronRight className="um-row-chevron" aria-hidden="true" />,
    },
  ]

  return (
    <div className="um-page">
      <PageToolbar
        meta={users.length ? <span>{users.length} 个用户</span> : undefined}
        actions={addUserButton}
      />
      <RequestState loading={loading} error={loadError} hasData={users.length > 0} empty={!loading && !loadError && users.length === 0} emptyDescription="暂无用户" onRetry={load}>
        <Table<User>
          rowKey="id"
          className="um-table"
          rowClassName="um-clickable-row"
          onRow={(user) => ({ onClick: (event) => {
            if (event.defaultPrevented || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey || window.getSelection()?.toString()) return
            navigate(`/admin/users/${user.id}`)
          } })}
          dataSource={users}
          columns={columns}
          scroll={{ x: 'max-content' }}
        />
      </RequestState>

      {open && <UserAccountDialog onClose={() => setOpen(false)} onSaved={(user) => navigate(`/admin/users/${user.id}`)} />}
    </div>
  )
}
