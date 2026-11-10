import type { ReactNode } from 'react'
import { Form, Input, Modal, Select, Table, type FormInstance } from '../../../ui'
import type { ImportSummary } from '../../../api'
import '../../../styles/forms.css'
import { MODAL_LG, MODAL_SM } from '../../../ui/modal'

function PreviewGroup({ title, count, children }: { title: string; count: ReactNode; children: ReactNode }) {
  return (
    <section className="fm-group">
      <h3 className="fm-group-title">{title}<small>{count}</small></h3>
      {children}
    </section>
  )
}

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
      className="fm-modal"
      title="导入服务器配置"
      open={open}
      onCancel={onCancel}
      onOk={onConfirm}
      okText="确认导入"
      confirmLoading={loading}
      width={MODAL_LG}
    >
      {summary && (
        <div className="fm-preview">
          <PreviewGroup title="入站" count={summary.inbounds?.length || 0}>
            <Table
              size="small"
              rowKey="tag"
              dataSource={summary.inbounds || []}
              columns={[
                { title: '标签', dataIndex: 'tag' },
                { title: '协议', dataIndex: 'type', render: (value) => <span className="ui-tag">{value}</span> },
                { title: '端口', dataIndex: 'listen_port', render: (value) => <span className="mono">{value}</span> },
                {
                  title: '模式',
                  render: (_, row: { single_user: boolean; users: number }) => row.single_user
                    ? <span className="muted">单用户</span>
                    : <span>多用户 <span className="mono">{row.users}</span></span>,
                },
              ]}
            />
          </PreviewGroup>
          <PreviewGroup title="出站" count={summary.outbounds?.length || 0}>
            <Table
              size="small"
              rowKey="tag"
              dataSource={summary.outbounds || []}
              columns={[
                { title: '标签', dataIndex: 'tag' },
                { title: '类型', dataIndex: 'type', render: (value) => <span className="ui-tag">{value}</span> },
                { title: '目标地址', dataIndex: 'info', render: (value) => <span className="mono">{value}</span> },
              ]}
            />
          </PreviewGroup>
          <PreviewGroup title="分流规则" count={summary.rules?.length || 0}>
            <Table
              size="small"
              rowKey={(_, index) => String(index)}
              dataSource={summary.rules || []}
              columns={[
                { title: '匹配入站', render: (_, row: { inbound: string[] | null }) => row.inbound?.length ? row.inbound.join(', ') : <span className="faint">不限</span> },
                { title: '其它匹配', dataIndex: 'info', render: (value) => value || <span className="faint">—</span> },
                { title: '出站', dataIndex: 'outbound', render: (value) => <span className="mono">{value}</span> },
              ]}
            />
          </PreviewGroup>
          <div className="fm-preview-final">默认出站<span className="ui-tag mono">{summary.final}</span></div>
          {!!summary.skipped?.length && (
            <div className="fm-preview-skipped">
              以下内容不会转为表单，仅保存在原始配置中：
              <ul>{summary.skipped.map((item) => <li key={item}>{item}</li>)}</ul>
            </div>
          )}
        </div>
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
    <Modal className="fm-modal" title="编辑配置" open={open} onCancel={onCancel} onOk={onSave} okText="校验并下发" okButtonProps={{ disabled: loading }} confirmLoading={saving} width={MODAL_LG}>
      {loading ? (
        <div className="fm-loading">读取中…</div>
      ) : (
        <Input.TextArea
          className="fm-code"
          aria-label="config.json"
          spellCheck={false}
          value={text}
          disabled={saving}
          onChange={(event) => onText(event.target.value)}
          autoSize={false} rows={18}
          placeholder="粘贴完整 config.json"
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
  const method = Form.useWatch('method', form) ?? 'script'
  return (
    <Modal className="fm-modal" title="安装 / 升级 sing-box" open={open} onOk={onConfirm} onCancel={onCancel} okText="开始安装" width={MODAL_SM}>
      <Form form={form} layout="vertical" className="fm-form" initialValues={{ channel: 'stable', method: 'script' }}>
        <div className="fm-grid" style={{ paddingTop: 6 }}>
          <Form.Item name="channel" label="渠道"><Select options={[{ value: 'stable', label: 'stable' }, { value: 'beta', label: 'beta' }]} /></Form.Item>
          <Form.Item name="method" label="安装方式">
            <Select onChange={(value) => {
              if (value !== 'script') form.setFieldsValue({ version: undefined })
            }} options={[
              { value: 'script', label: '官方脚本' },
              { value: 'apt', label: 'APT 源' },
              { value: 'dnf', label: 'DNF 源' },
            ]} />
          </Form.Item>
          <Form.Item name="version" label="版本" className="fm-full" extra={method !== 'script' ? '指定版本请使用官方脚本' : undefined}>
            <Input disabled={method !== 'script'} placeholder={method === 'script' ? '最新' : '使用软件源最新版本'} className="mono" />
          </Form.Item>
        </div>
      </Form>
    </Modal>
  )
}

export function ServerEditModal({
  title, open, saving, form, onCancel, onConfirm,
}: {
  title: string
  open: boolean
  saving: boolean
  form: FormInstance
  onCancel: () => void
  onConfirm: () => void
}) {
  return (
    <Modal
      title={title}
      width={MODAL_SM}
      open={open}
      onOk={onConfirm}
      onCancel={onCancel}
      confirmLoading={saving}
    >
      <Form form={form} layout="vertical" initialValues={{ traffic_reset_day: 1 }}>
        <Form.Item name="name" label="名称" rules={[{ required: true }]}>
          <Input placeholder="hk-1" />
        </Form.Item>
        <Form.Item name="address" label="连接地址">
          <Input placeholder="hk.example.com" />
        </Form.Item>
        <Form.Item name="remark" label="备注">
          <Input />
        </Form.Item>
        <Form.Item name="traffic_reset_day" label="流量重置日" extra="每月当天 UTC 00:00 重新计量；短月按月末，重置前流量不计入页面，历史记录保留。" rules={[{ required: true }]}>
          <Select options={Array.from({ length: 31 }, (_, index) => ({ value: index + 1, label: `每月 ${index + 1} 日` }))} />
        </Form.Item>
      </Form>
    </Modal>
  )
}
