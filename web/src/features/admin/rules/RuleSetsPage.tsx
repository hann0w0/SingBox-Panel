import { useCallback, useEffect, useRef, useState } from 'react'
import { Button, Form, Grid, Input, Modal, Segmented, Table, message } from '../../../ui'
import { Pencil, Plus, Trash2 } from 'lucide-react'
import {
  createSharedRuleSet, deleteSharedRuleSet, errMsg, isCanceledRequest,
  listServers, listSharedRuleSets, updateSharedRuleSet,
} from '../../../api'
import type { RuleSetBody } from '../../../api'
import type { RuleSet, Server } from '../../../types'
import { PageHeader } from '../../../ui/Page'
import { RequestState } from '../../../components/RequestState'
import { MODAL_SM } from '../../../ui/modal'
import '../../../styles/users.css'
import '../../../styles/rules.css'

/** Panel-wide rule-set definitions shared by any number of servers. */
export default function RuleSetsPage() {
  const screens = Grid.useBreakpoint()
  const [ruleSets, setRuleSets] = useState<RuleSet[]>([])
  const [servers, setServers] = useState<Server[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<RuleSet | null>(null)
  const [open, setOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [rsType, setRsType] = useState<'remote' | 'local'>('remote')
  const [form] = Form.useForm()
  const loadRef = useRef<{ generation: number; controller: AbortController | null }>({ generation: 0, controller: null })

  const load = useCallback(() => {
    loadRef.current.controller?.abort()
    const controller = new AbortController()
    const generation = ++loadRef.current.generation
    loadRef.current.controller = controller
    setLoading(true)
    setError(null)
    Promise.all([listSharedRuleSets(controller.signal), listServers(controller.signal)])
      .then(([sets, hosts]) => {
        if (generation !== loadRef.current.generation) return
        setRuleSets(sets)
        setServers(hosts)
      })
      .catch((cause) => {
        if (generation === loadRef.current.generation && !isCanceledRequest(cause)) setError(errMsg(cause))
      })
      .finally(() => {
        if (generation === loadRef.current.generation) {
          loadRef.current.controller = null
          setLoading(false)
        }
      })
  }, [])

  useEffect(() => {
    load()
    return () => {
      loadRef.current.generation++
      loadRef.current.controller?.abort()
    }
  }, [load])

  const openCreate = () => {
    setEditing(null)
    setRsType('remote')
    form.resetFields()
    form.setFieldsValue({ type: 'remote', format: 'binary', update_interval: '1d' })
    setOpen(true)
  }

  const openEdit = (rs: RuleSet) => {
    setEditing(rs)
    setRsType(rs.type || 'remote')
    form.setFieldsValue({
      tag: rs.tag, type: rs.type || 'remote', format: rs.format || 'binary',
      url: rs.url || '', path: rs.path || '', update_interval: rs.update_interval || '1d',
    })
    setOpen(true)
  }

  const submit = async () => {
    setSaving(true)
    try {
      const values = await form.validateFields() as RuleSetBody
      if (editing) await updateSharedRuleSet(editing.id, values)
      else await createSharedRuleSet(values)
      setOpen(false)
      load()
    } catch (cause) {
      message.error(errMsg(cause))
    } finally {
      setSaving(false)
    }
  }

  const remove = (rs: RuleSet) => Modal.confirm({
    title: '删除规则集',
    content: `确认删除「${rs.tag}」？`,
    okText: '删除',
    okType: 'danger',
    onOk: async () => {
      await deleteSharedRuleSet(rs.id)
      load()
    },
  })

  const serverReferences = (rs: RuleSet) => {
    const names = (rs.server_ids ?? []).map((id) => servers.find((server) => server.id === id)?.name ?? `#${id}`)
    if (!names.length) return <span className="faint">未引用</span>
    const visibleNames = screens.md ? names.slice(0, 3) : names
    return <span className="nodes-ruleset-servers" title={names.join('、')}>
      {visibleNames.map((name, index) => <span key={rs.server_ids?.[index]} className="ui-tag">{name}</span>)}
      {names.length > visibleNames.length && <span className="faint">+{names.length - visibleNames.length}</span>}
    </span>
  }

  const rowActions = (rs: RuleSet) => <span className="ui-row-actions">
    <Button size="small" type="text" icon={<Pencil />} aria-label={`编辑规则集 ${rs.tag}`} onClick={() => openEdit(rs)} />
    <Button size="small" type="text" icon={<Trash2 />} aria-label={`删除规则集 ${rs.tag}`} onClick={() => remove(rs)} />
  </span>

  return (
    <div className="um-page rs-page">
      <PageHeader
        title="规则"
        actions={<Button type="primary" icon={<Plus />} onClick={openCreate}>新增规则集</Button>}
      />
      <RequestState loading={loading} error={error} hasData={ruleSets.length > 0} empty={!loading && !error && ruleSets.length === 0} emptyDescription="暂无规则集" onRetry={load}>
        {screens.md ? <Table<RuleSet>
          rowKey="id"
          className="rs-table"
          dataSource={ruleSets}
          columns={[
            { title: '标签', dataIndex: 'tag', render: (value: string) => <span className="um-strong">{value}</span> },
            { title: '类型', dataIndex: 'type', width: 90, render: (value: string) => <span className="ui-tag">{value === 'local' ? '本地' : '远程'}</span> },
            { title: '格式', dataIndex: 'format', width: 90, render: (value: string) => <span className="mono">{value === 'source' ? 'JSON' : 'SRS'}</span> },
            {
              title: '服务器',
              render: (_: unknown, rs: RuleSet) => serverReferences(rs),
            },
            {
              title: <span className="sr-only">操作</span>, width: 96, align: 'right',
              render: (_: unknown, rs: RuleSet) => rowActions(rs),
            },
          ]}
        /> : (
          <ul className="ui-card rs-list" aria-label="规则集">
            {ruleSets.map((rs) => <li key={rs.id} className="rs-list-item">
              <div className="rs-list-main">
                <strong className="um-strong rs-list-tag">{rs.tag}</strong>
                <div className="rs-list-meta">
                  <span className="ui-tag">{rs.type === 'local' ? '本地' : '远程'}</span>
                  <span className="mono">{rs.format === 'source' ? 'JSON' : 'SRS'}</span>
                </div>
              </div>
              {rowActions(rs)}
              <div className="rs-list-servers" role="group" aria-label="引用服务器">{serverReferences(rs)}</div>
            </li>)}
          </ul>
        )}
      </RequestState>

      <Modal title={editing ? '编辑规则集' : '新增规则集'} width={MODAL_SM} open={open} onOk={submit} onCancel={() => setOpen(false)} confirmLoading={saving}>
        <Form form={form} layout="vertical">
          <Form.Item name="tag" label="标签" rules={[
            { required: true, message: '请输入规则集标签' },
            { pattern: /^[a-zA-Z0-9_-]+$/, message: '标签只能包含字母、数字、下划线与连字符' },
          ]}>
            <Input placeholder="geosite-cn" />
          </Form.Item>
          <Form.Item name="type" label="类型">
            <Segmented<'remote' | 'local'> onChange={setRsType} options={[{ value: 'remote', label: '远程' }, { value: 'local', label: '本地' }]} />
          </Form.Item>
          <Form.Item name="format" label="格式">
            <Segmented options={[{ value: 'binary', label: 'SRS' }, { value: 'source', label: 'JSON' }]} />
          </Form.Item>
          {rsType === 'remote' ? <>
            <Form.Item name="url" label="URL" rules={[{ required: true, message: '请输入远程规则集下载 URL' }]}>
              <Input placeholder="https://" />
            </Form.Item>
            <Form.Item name="update_interval" label="更新间隔">
              <Input placeholder="1d" />
            </Form.Item>
          </> : (
            <Form.Item name="path" label="路径" rules={[{ required: true, message: '请输入服务器上的本地文件路径' }]}>
              <Input placeholder="/etc/sing-box/rules.json" />
            </Form.Item>
          )}
        </Form>
      </Modal>
    </div>
  )
}
