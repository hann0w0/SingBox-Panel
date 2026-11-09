import { useEffect, useRef, useState } from 'react'
import { Plus } from 'lucide-react'
import { errMsg, listRoutingNodes, listRuleSets } from '../../../api'
import type { RoutingNode, RuleSet } from '../../../types'
import { Button } from '../../../ui'
import { PageHeader } from '../../../ui/Page'
import { RequestState } from '../../../components/RequestState'
import { RuleSetsTab } from './RuleSetsTab'

export default function RuleSets() {
  const [ruleSets, setRuleSets] = useState<RuleSet[]>([])
  const [nodes, setNodes] = useState<RoutingNode[]>([])
  const [loading, setLoading] = useState(true)
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [createSignal, setCreateSignal] = useState(0)
  const request = useRef<AbortController | null>(null)

  const load = () => {
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    setLoading(true)
    setError(null)
    Promise.all([listRuleSets(controller.signal), listRoutingNodes(controller.signal)])
      .then(([rs, ns]) => {
        if (controller.signal.aborted) return
        setRuleSets(rs)
        setNodes(ns)
        setLoaded(true)
      })
      .catch((e) => { if (!controller.signal.aborted) setError(errMsg(e)) })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
  }
  useEffect(() => {
    load()
    return () => request.current?.abort()
  }, [])

  return <div className="um-page">
    <PageHeader title="规则集" actions={<Button type="primary" icon={<Plus />} disabled={!loaded} onClick={() => setCreateSignal((n) => n + 1)}>新增规则集</Button>} />
    <RequestState loading={loading} error={error} hasData={loaded} onRetry={load}>
      <RuleSetsTab ruleSets={ruleSets} nodes={nodes} onRefresh={load} createSignal={createSignal} />
    </RequestState>
  </div>
}
