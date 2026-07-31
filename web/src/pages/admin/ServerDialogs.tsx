import { Form, Input, Modal, Select, Space, Table, Tag, type FormInstance } from 'antd'
import type { ImportSummary, NodeFormats } from '../../api'
import { NodeFormatsModalContent } from './NodeFormatsModal'

export function ImportPreviewModal({
  open, loading, summary, onCancel, onConfirm,
}: {
  open: boolean
  loading: boolean
  summary: ImportSummary | null
  onCancel: () => void
  onConfirm: () => void
}) {
  return (
    <Modal
      title="识别服务器配置"
      open={open}
      onCancel={onCancel}
      onOk={onConfirm}
      okText="确认导入面板"
      confirmLoading={loading}
      width={720}
      destroyOnClose
    >
      <div style={{ fontSize: 13, color: 'rgba(0, 0, 0, 0.45)', marginBottom: 12 }}>
        已识别当前运行配置。导入后完整原始配置将同步保存至面板。
      </div>
      {summary && (
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <div>
            <b>入站协议 {summary.inbounds?.length || 0} 个</b>
            <Table
              size="small"
              rowKey="tag"
              pagination={false}
              dataSource={summary.inbounds || []}
              columns={[
                { title: '标签', dataIndex: 'tag' },
                { title: '协议', dataIndex: 'type', render: (value) => <Tag>{value}</Tag> },
                { title: '端口', dataIndex: 'listen_port' },
                {
                  title: '模式',
                  render: (_, row: { single_user: boolean; users: number }) => row.single_user
                    ? <Tag color="green">单用户</Tag>
                    : <Tag color="orange">多用户 {row.users}</Tag>,
                },
              ]}
            />
          </div>
          <div>
            <b>出站 {summary.outbounds?.length || 0} 个</b>
            <Table
              size="small"
              rowKey="tag"
              pagination={false}
              dataSource={summary.outbounds || []}
              columns={[
                { title: '标签', dataIndex: 'tag' },
                { title: '类型', dataIndex: 'type', render: (value) => <Tag>{value}</Tag> },
                { title: '目标地址', dataIndex: 'info' },
              ]}
            />
          </div>
          <div>
            <b>分流规则 {summary.rules?.length || 0} 条</b>
            <Table
              size="small"
              rowKey={(_, index) => String(index)}
              pagination={false}
              dataSource={summary.rules || []}
              columns={[
                { title: '匹配入站', render: (_, row: { inbound: string[] | null }) => row.inbound?.length ? row.inbound.join(', ') : '不限' },
                { title: '其它匹配', dataIndex: 'info', render: (value) => value || '—' },
                { title: '→ 出站', dataIndex: 'outbound', render: (value) => <Tag color="blue">{value}</Tag> },
              ]}
            />
          </div>
          <div style={{ color: '#555' }}>默认出站 final：<Tag>{summary.final}</Tag></div>
          {!!summary.skipped?.length && (
            <div style={{ color: '#d46b08' }}>
              以下内容不会转换成结构化表单，但会保存在完整原始配置中：
              <ul style={{ margin: '4px 0 0 18px' }}>{summary.skipped.map((item) => <li key={item}>{item}</li>)}</ul>
            </div>
          )}
        </Space>
      )}
    </Modal>
  )
}

export function ConfigEditorModal({
  open, loading, saving, text, onText, onCancel, onSave,
}: {
  open: boolean
  loading: boolean
  saving: boolean
  text: string
  onText: (value: string) => void
  onCancel: () => void
  onSave: () => void
}) {
  return (
    <Modal title="编辑服务器配置" open={open} onCancel={onCancel} onOk={onSave} okText="校验并下发" confirmLoading={saving} width={780} destroyOnClose>
      <div style={{ fontSize: 13, color: 'rgba(0, 0, 0, 0.45)', marginBottom: 12 }}>
        直接编辑该服务器运行的核心配置。下发后会自动同步；能够无损转换时将直接进入面板管理。
      </div>
      {loading ? (
        <div style={{ padding: 24, textAlign: 'center', color: '#94a3b8' }}>读取中…</div>
      ) : (
        <Input.TextArea
          value={text}
          onChange={(event) => onText(event.target.value)}
          autoSize={{ minRows: 18, maxRows: 30 }}
          style={{ fontFamily: 'monospace', fontSize: 12 }}
          placeholder="该服务器暂无配置，可在此粘贴或编写完整 config.json"
        />
      )}
    </Modal>
  )
}

export function InstallSingboxModal({
  open, form, onCancel, onConfirm,
}: {
  open: boolean
  form: FormInstance
  onCancel: () => void
  onConfirm: () => void
}) {
  return (
    <Modal title="安装 / 升级 Sing-box" open={open} onOk={onConfirm} onCancel={onCancel} destroyOnClose>
      <div style={{ fontSize: 13, color: 'rgba(0, 0, 0, 0.45)', marginBottom: 12 }}>
        安装官方最新稳定版 sing-box；已安装时再次执行即升级到目标版本。
      </div>
      <Form form={form} layout="vertical" initialValues={{ channel: 'stable', method: 'script' }}>
        <Form.Item name="channel" label="渠道"><Select options={[{ value: 'stable', label: 'stable' }, { value: 'beta', label: 'beta' }]} /></Form.Item>
        <Form.Item name="method" label="安装方式">
          <Select options={[
            { value: 'script', label: '官方安装脚本' },
            { value: 'apt', label: '官方 APT 源' },
            { value: 'dnf', label: '官方 DNF 源' },
          ]} />
        </Form.Item>
        <Form.Item name="version" label="指定版本"><Input placeholder="1.13.14" /></Form.Item>
      </Form>
    </Modal>
  )
}

export function NodeFormatsExportModal({
  open, loading, data, onCancel,
}: {
  open: boolean
  loading: boolean
  data: NodeFormats | null
  onCancel: () => void
}) {
  return (
    <Modal title="导出节点配置" open={open} onCancel={onCancel} footer={null} width={860} destroyOnClose>
      {loading ? <div style={{ padding: 48, textAlign: 'center', color: '#bbb' }}>加载中…</div> : data ? <NodeFormatsModalContent data={data} /> : null}
    </Modal>
  )
}
