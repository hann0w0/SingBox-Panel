import React, { useRef, useState } from 'react'
import { Button, Form, Input, Modal, Radio, Select, Space, Table, Tag, message } from 'antd'
import { DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons'
import { createRuleSet, deleteRuleSet, updateRuleSet } from '../../../api'
import { showConfigApplyResult } from '../../../configApply'
import type { Outbound, RuleSet } from '../../../types'

interface RuleSetsTabProps {
  serverId: number
  ruleSets: RuleSet[]
  outbounds: Outbound[]
  onRefresh: () => void
  rawMode?: boolean
}

export const RuleSetsTab: React.FC<RuleSetsTabProps> = ({ serverId, ruleSets, outbounds, onRefresh, rawMode }) => {
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
      download_detour: rs.download_detour || 'direct',
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
      title: `确定删除规则集 ${rs.tag}？`,
      okText: '确定',
      cancelText: '取消',
      okType: 'danger',
      centered: true,
      onOk: () => handleDelete(rs),
    })
  }

  const columns = [
    {
      title: '标签 (Tag)',
      dataIndex: 'tag',
      key: 'tag',
      render: (tag: string) => <Tag color="cyan">{tag}</Tag>,
    },
    {
      title: '类型',
      dataIndex: 'type',
      key: 'type',
      width: 100,
      render: (type: string) => (
        <Tag color={type === 'local' ? 'purple' : 'blue'}>{type === 'local' ? '本地' : '远程'}</Tag>
      ),
    },
    {
      title: '格式',
      dataIndex: 'format',
      key: 'format',
      width: 110,
      render: (format: string) => (
        <Tag color={format === 'source' ? 'orange' : 'green'}>{format === 'source' ? 'JSON' : 'SRS 二进制'}</Tag>
      ),
    },
    {
      title: '更新间隔',
      dataIndex: 'update_interval',
      key: 'update_interval',
      width: 100,
      render: (val: string, record: RuleSet) => (record.type === 'local' ? '-' : val || '1d'),
    },
    {
      title: '操作',
      key: 'actions',
      width: 110,
      render: (_: any, record: RuleSet) => (
        <Space size={0}>
          <Button type="link" size="small" disabled={rawMode} icon={<EditOutlined />} onClick={() => openEditModal(record)}>
            编辑
          </Button>
          <Button type="link" size="small" danger disabled={rawMode} icon={<DeleteOutlined />} onClick={() => confirmDeleteRS(record)}>
            删除
          </Button>
        </Space>
      ),
    },
  ]

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
        <span style={{ fontSize: 13, color: '#666' }}>
          配置规则集 (Rule Sets)，用于规则精确匹配域名或 IP 资源。
        </span>
        <Button type="primary" size="small" icon={<PlusOutlined />} disabled={rawMode} onClick={openCreateModal}>
          新增规则集
        </Button>
      </div>

      <Table dataSource={ruleSets} columns={columns} rowKey="id" pagination={false} size="small" />

      <Modal
        title={editingRS ? '编辑规则集' : '新增规则集'}
        open={modalOpen}
        onOk={handleSubmit}
        onCancel={() => setModalOpen(false)}
        confirmLoading={loading}
        centered
        destroyOnClose
      >
        <Form form={form} layout="vertical" initialValues={{ type: 'remote', format: 'binary', update_interval: '1d' }}>
          <Form.Item
            name="tag"
            label="规则集标签 (Tag)"
            rules={[
              { required: true, message: '请输入规则集标签' },
              { pattern: /^[a-zA-Z0-9_-]+$/, message: '标签只能包含字母、数字、下划线与连字符' },
            ]}
          >
            <Input placeholder="请输入规则集标签" />
          </Form.Item>

          <Form.Item name="type" label="类型">
            <Radio.Group onChange={(e) => setRsType(e.target.value)}>
              <Radio.Button value="remote">远程规则集 (Remote)</Radio.Button>
              <Radio.Button value="local">本地规则集 (Local)</Radio.Button>
            </Radio.Group>
          </Form.Item>

          <Form.Item name="format" label="格式">
            <Radio.Group>
              <Radio.Button value="binary">SRS 二进制 (.srs)</Radio.Button>
              <Radio.Button value="source">JSON 源码 (.json)</Radio.Button>
            </Radio.Group>
          </Form.Item>

          {rsType === 'remote' ? (
            <>
              <Form.Item
                name="url"
                label="远程下载 URL"
                rules={[{ required: true, message: '请输入远程规则集下载 URL' }]}
              >
                <Input placeholder="请输入远程规则集下载 URL" />
              </Form.Item>

              <Form.Item name="update_interval" label="更新间隔">
                <Input placeholder="请输入更新间隔 (如 1d / 12h)" />
              </Form.Item>

              <Form.Item name="download_detour" label="下载出站" extra="规则集文件将通过该出站下载。">
                <Select
                  options={[
                    { value: 'direct', label: 'direct · 内置直连' },
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
              label="本地文件路径 (Path)"
              rules={[{ required: true, message: '请输入服务器上的本地文件路径' }]}
            >
              <Input placeholder="请输入绝对路径" />
            </Form.Item>
          )}
        </Form>
      </Modal>
    </div>
  )
}
