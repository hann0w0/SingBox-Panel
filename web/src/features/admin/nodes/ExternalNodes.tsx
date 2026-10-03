import { useEffect, useRef, useState } from 'react'
import { errMsg, listCustomNodes } from '../../../api'
import type { CustomNode } from '../../../api'
import { CustomNodesPanel } from '../access/Access'

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
    <div className="users-page external-nodes-page">
      <CustomNodesPanel nodes={nodes} loading={loading} error={error} onNodesChange={load} />
    </div>
  )
}
