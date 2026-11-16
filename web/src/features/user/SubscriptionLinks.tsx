import { Copy } from 'lucide-react'
import { Modal, Typography, message } from '../../components/kit'
import { copyToClipboard } from '../../lib/util'
import '../../styles/settings-user.css'

const CLIENTS = [
  { kind: 'surge', label: 'Surge', logo: 'surge.png', target: 'surge' },
  { kind: 'clashmeta', label: 'ClashMeta', logo: 'clashmeta.png', target: 'clash' },
  { kind: 'shadowrocket', label: 'Shadowrocket', logo: 'shadowrocket.png', target: 'shadowrocket' },
]

export function SubscriptionLinks({ url, pending = false }: { url: string; pending?: boolean }) {
  const copy = async (client: typeof CLIENTS[number]) => {
    const link = `${url}?target=${client.target}`
    try {
      await copyToClipboard(link)
      message.success(`已复制 ${client.label} 订阅链接${pending ? '，请保存节点更改后再更新订阅' : ''}`)
    } catch {
      Modal.info({ title: `${client.label} 订阅链接`, width: 640, content: <Typography.Paragraph copyable code style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{link}</Typography.Paragraph> })
    }
  }
  return <div className="sub-clients">
    {CLIENTS.map((client) => <button key={client.kind} type="button" className="sub-client" disabled={!url} onClick={() => void copy(client)} aria-label={`复制 ${client.label} 订阅链接`}>
      <span className="sub-client-logo" aria-hidden="true"><img src={`/logos/${client.logo}?v=20260906-2`} alt="" width={28} height={28} draggable={false} /></span>
      <span className="sub-client-name">{client.label}</span>
      <span className="sub-client-copy"><Copy /> 复制</span>
    </button>)}
  </div>
}
