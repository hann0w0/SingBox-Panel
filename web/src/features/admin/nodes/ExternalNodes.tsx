import { useEffect, useRef, useState } from 'react'
import { errMsg, listCustomNodes } from '../../../lib/api'
import type { CustomNode } from '../../../lib/api'
import { CustomNodesPanel } from '../access/CustomNodesPanel'
import { PageHeader } from '../../../components/kit/Page'
import '../../../styles/users.css'

// ExternalNodes manages third-party subscriptions and hand-added nodes that
// are merged into user subscriptions but not hosted on panel servers.
export default function ExternalNodes() {
  const [nodes, setNodes] = useState<CustomNode[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const loadRef = useRef<{ generation: number; controller: AbortController | null }>({ generation: 0, controller: null })

  const load = () => {
    loadRef.current.controller?.abort()
    const controller = new AbortController()
    const generation = ++loadRef.current.generation
    loadRef.current.controller = controller
    setLoading(true)
    setError(null)
    listCustomNodes(controller.signal)
      .then((next) => {
        if (generation === loadRef.current.generation) setNodes(next)
      })
      .catch((e) => {
        if (generation === loadRef.current.generation && !controller.signal.aborted) setError(errMsg(e))
      })
      .finally(() => {
        if (generation === loadRef.current.generation) {
          loadRef.current.controller = null
          setLoading(false)
        }
      })
  }
  useEffect(() => {
    load()
    return () => {
      loadRef.current.generation++
      loadRef.current.controller?.abort()
    }
  }, [])

  return (
    <div className="um-page">
      <PageHeader title="节点" />
      <CustomNodesPanel nodes={nodes} loading={loading} error={error} onNodesChange={load} />
    </div>
  )
}
