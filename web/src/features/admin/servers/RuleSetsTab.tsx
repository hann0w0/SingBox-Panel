import React, { useEffect, useRef, useState } from 'react'
import { Button, Form, Input, Modal, Segmented, Select, Table, message } from '../../../ui'
import { Trash2, Pencil } from 'lucide-react'
import { createRuleSet, deleteRuleSet, updateRuleSet } from '../../../api'
import { showConfigApplyResult } from '../../../configApply'
import type { Outbound, RuleSet } from '../../../types'
import { MODAL_SM } from '../../../ui/modal'

// Keep the empty API value out of the selector without colliding with string tags.
const DEFAULT_DOWNLOAD_DETOUR = 0

interface RuleSetsTabProps {
  serverId: number
  ruleSets: RuleSet[]
  outbounds: Outbound[]
  onRefresh: () => void
  rawMode?: boolean
  /** Incrementing this opens the create dialog (the add button lives in the page toolbar). */
  createSignal?: number
}

export const RuleSetsTab: React.FC<RuleSetsTabProps> = ({ serverId, ruleSets, outbounds, onRefresh, rawMode, createSignal = 0 }) => {
  const [modalOpen, setModalOpen] = useState(false)
  const [editingRS, setEditingRS] = useState<RuleSet | null>(null)
  const [loading, setLoading] = useState(false)
  const submitLock = useRef(false)
  const [form] = Form.useForm()
  const [rsType, setRsType] = useState<'remote' | 'local'>('remote')

  const openCreateModal = () => {
    setEditingRS(null)
    setRsType('remote')
    form.resetFields()
    form.setFieldsValue({
      type: 'remote',
      format: 'binary',
      update_interval: '1d',
      download_detour: 'direct',
    })
    setModalOpen(true)
  }

  const openEditModal = (rs: RuleSet) => {
    setEditingRS(rs)
    setRsType(rs.type || 'remote')
    form.setFieldsValue({
      tag: rs.tag,
      type: rs.type || 'remote',
      format: rs.format || 'binary',
      url: rs.url || '',
      path: rs.path || '',
      update_interval: rs.update_interval || '1d',
      download_detour: rs.download_detour ?? '',
    })
    setModalOpen(true)
  }

  const handleSubmit = async () => {
    if (submitLock.current) return
    submitLock.current = true
    try {
      const values = await form.validateFields()
      setLoading(true)
      const payload = values
      if (editingRS) {
        const result = await updateRuleSet(serverId, editingRS.id, payload)
        showConfigApplyResult(result)
      } else {
        const result = await createRuleSet(serverId, payload)
        showConfigApplyResult(result)
      }
      setModalOpen(false)
      onRefresh()
    } catch (err: any) {
      if (err?.response?.data?.error) {
        message.error(err.response.data.error)
      } else if (err?.message) {
        message.error(err.message)
      }
    } finally {
      submitLock.current = false
      setLoading(false)
    }
  }

  const handleDelete = async (rs: RuleSet) => {
    try {
      const result = await deleteRuleSet(serverId, rs.id)
      showConfigApplyResult(result)
      onRefresh()
    } catch (err: any) {
      message.error(err?.response?.data?.error || '删除规则集失败')
    }
  }

  const confirmDeleteRS = (rs: RuleSet) => {
    Modal.confirm({
      title: '删除规则集',
      content: `确认删除「${rs.tag}」？`,
      okText: '删除',
      cancelText: '取消',
      okType: 'danger',
      onOk: () => handleDelete(rs),
    })
  }

  const lastCreateSignal = useRef(createSignal)
  useEffect(() => {
    if (createSignal !== lastCreateSignal.current) {
      lastCreateSignal.current = createSignal
      openCreateModal()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [createSignal])

  const columns = [
    {
      title: '标签',
      dataIndex: 'tag',
      key: 'tag',
      render: (tag: string) => <span className="sd-strong">{tag}</span>,
    },
    {
      title: '类型',
      dataIndex: 'type',
      key: 'type',
      width: 100,
      render: (type: string) => <span className="ui-tag">{type === 'local' ? '本地' : '远程'}</span>,
    },
    {
      title: '格式',
      dataIndex: 'format',
      key: 'format',
      width: 110,
      render: (format: string) => <span className="mono">{format === 'source' ? 'JSON' : 'SRS'}</span>,
    },
    {
      title: '更新间隔',
      dataIndex: 'update_interval',
      key: 'update_interval',
      width: 100,
      render: (val: string, record: RuleSet) => <span className="mono">{record.type === 'local' ? '—' : val || '1d'}</span>,
    },
    {
      title: '',
      key: 'actions',
      width: 88,
      align: 'right' as const,
      render: (_: any, record: RuleSet) => (
        <span className="ui-row-actions">
          <Button type="text" size="small" disabled={rawMode} icon={<Pencil />} aria-label={`编辑规则集 ${record.tag}`} onClick={() => openEditModal(record)} />
          <Button type="text" size="small" disabled={rawMode} icon={<Trash2 />} aria-label={`删除规则集 ${record.tag}`} onClick={() => confirmDeleteRS(record)} />
        </span>
      ),
    },
  ]

  return (
    <div>
      <Table dataSource={ruleSets} columns={columns} rowKey="id" locale={{ emptyText: '暂无规则集' }} />

      <Modal
        title={editingRS ? '编辑规则集' : '新增规则集'}
        width={MODAL_SM}
        open={modalOpen}
        onOk={handleSubmit}
        onCancel={() => setModalOpen(false)}
        confirmLoading={loading}
      >
        <Form form={form} layout="vertical" initialValues={{ type: 'remote', format: 'binary', update_interval: '1d' }}>
          <Form.Item
            name="tag"
            label="标签"
            rules={[
              { required: true, message: '请输入规则集标签' },
              { pattern: /^[a-zA-Z0-9_-]+$/, message: '标签只能包含字母、数字、下划线与连字符' },
            ]}
          >
            <Input placeholder="geosite-cn" />
          </Form.Item>

          <Form.Item name="type" label="类型">
            <Segmented<'remote' | 'local'> onChange={(v) => setRsType(v)} options={[{ value: 'remote', label: '远程' }, { value: 'local', label: '本地' }]} />
          </Form.Item>

          <Form.Item name="format" label="格式">
            <Segmented options={[{ value: 'binary', label: 'SRS' }, { value: 'source', label: 'JSON' }]} />
          </Form.Item>

          {rsType === 'remote' ? (
            <>
              <Form.Item
                name="url"
                label="URL"
                rules={[{ required: true, message: '请输入远程规则集下载 URL' }]}
              >
                <Input placeholder="https://" />
              </Form.Item>

              <Form.Item name="update_interval" label="更新间隔">
                <Input placeholder="1d" />
              </Form.Item>

              <Form.Item
                name="download_detour"
                label="下载出站"
                getValueProps={(value: string | undefined) => ({ value: value || DEFAULT_DOWNLOAD_DETOUR })}
                getValueFromEvent={(value: string | typeof DEFAULT_DOWNLOAD_DETOUR) => value === DEFAULT_DOWNLOAD_DETOUR ? '' : value}
              >
                <Select
                  options={[
                    { value: DEFAULT_DOWNLOAD_DETOUR, label: '跟随默认出站' },
                    { value: 'direct', label: 'direct' },
                    ...outbounds.filter((outbound) => outbound.tag !== 'direct').map((outbound) => ({
                      value: outbound.tag,
                      label: outbound.tag,
                    })),
                  ]}
                />
              </Form.Item>
            </>
          ) : (
            <Form.Item
              name="path"
              label="路径"
              rules={[{ required: true, message: '请输入服务器上的本地文件路径' }]}
            >
              <Input placeholder="/etc/sing-box/rules.json" />
            </Form.Item>
          )}
        </Form>
      </Modal>
    </div>
  )
}
