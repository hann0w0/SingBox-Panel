import { Checkbox, Tag } from 'antd'
import type { Server } from '../types'

interface ServerAccessPickerProps {
  servers: Server[]
  value?: string[]
  onChange?: (value: string[]) => void
}

// ServerAccessPicker is a server -> inbound grant tree used when assigning
// nodes to a user. A checked server (or all its inbounds checked) means a
// whole-node grant that automatically covers protocols created later.
export default function ServerAccessPicker({ servers, value = [], onChange }: ServerAccessPickerProps) {
  const sKey = (id: number) => `s:${id}`
  const iKey = (id: number) => `i:${id}`

  const updateKeys = (newKeys: string[]) => {
    onChange?.(newKeys)
  }

  if (!servers.length) {
    return <div style={{ fontSize: 13, color: 'rgba(0, 0, 0, 0.45)', padding: '12px 0' }}>暂无可用节点</div>
  }

  return (
    <div style={{ maxHeight: 340, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 12, paddingRight: 4 }}>
      {servers.map((s) => {
        const ibs = s.inbounds ?? []
        const serverKey = sKey(s.id)

        const serverChecked = value.includes(serverKey)
        const selectedIbKeys = ibs.filter((ib) => value.includes(iKey(ib.id))).map((ib) => iKey(ib.id))
        const isAllIbsChecked = ibs.length > 0 && selectedIbKeys.length === ibs.length

        const isChecked = serverChecked || isAllIbsChecked
        const isIndeterminate = !isChecked && selectedIbKeys.length > 0

        const handleServerToggle = (checked: boolean) => {
          let next = value.filter((k) => k !== serverKey && !ibs.some((ib) => iKey(ib.id) === k))
          if (checked) {
            next.push(serverKey)
            next.push(...ibs.map((ib) => iKey(ib.id)))
          }
          updateKeys(next)
        }

        const handleInboundToggle = (ibId: number) => {
          const targetIkey = iKey(ibId)
          const currentlyChecked = selectedIbKeys.includes(targetIkey)
          let currentIbKeys = selectedIbKeys
          if (!currentlyChecked) {
            currentIbKeys = [...currentIbKeys, targetIkey]
          } else {
            currentIbKeys = currentIbKeys.filter((k) => k !== targetIkey)
          }

          let next = value.filter((k) => k !== serverKey && !ibs.some((ib) => iKey(ib.id) === k))
          if (currentIbKeys.length === ibs.length && ibs.length > 0) {
            next.push(serverKey)
            next.push(...currentIbKeys)
          } else {
            next.push(...currentIbKeys)
          }
          updateKeys(next)
        }

        return (
          <div
            key={s.id}
            style={{
              background: '#ffffff',
              border: '1px solid #e5e7eb',
              borderRadius: 8,
              padding: '12px 14px',
              boxShadow: '0 1px 2px 0 rgba(0, 0, 0, 0.03)',
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: '6px 8px', marginBottom: ibs.length ? 10 : 0 }}>
              <div
                style={{ cursor: 'pointer', display: 'inline-flex', alignItems: 'center', gap: 6, userSelect: 'none', minWidth: 0, flex: '1 1 auto' }}
                onClick={() => handleServerToggle(!isChecked)}
              >
                <Checkbox
                  checked={isChecked}
                  indeterminate={isIndeterminate}
                  onChange={(e) => handleServerToggle(e.target.checked)}
                />
                <span style={{ fontWeight: 600, fontSize: 13, color: '#111827', wordBreak: 'break-all' }}>
                  {s.name} {s.region ? `· ${s.region}` : ''}
                </span>
              </div>

              {isChecked ? (
                <Tag color="green" style={{ margin: 0, fontSize: 11, borderRadius: 10, padding: '0 8px', border: 'none', flexShrink: 0 }}>全节点授权（新协议自动同步）</Tag>
              ) : isIndeterminate ? (
                <Tag color="orange" style={{ margin: 0, fontSize: 11, borderRadius: 10, padding: '0 8px', border: 'none', flexShrink: 0 }}>部分授权 ({selectedIbKeys.length}/{ibs.length})</Tag>
              ) : (
                <Tag style={{ margin: 0, fontSize: 11, color: '#9ca3af', borderRadius: 10, padding: '0 8px', border: 'none', background: '#f3f4f6', flexShrink: 0 }}>未授权</Tag>
              )}
            </div>

            {ibs.length > 0 && (
              <div
                style={{
                  display: 'flex',
                  flexWrap: 'wrap',
                  gap: 8,
                  paddingLeft: 22,
                  paddingTop: 4,
                }}
              >
                {ibs.map((ib) => {
                  const ibChecked = value.includes(iKey(ib.id))
                  return (
                    <div
                      key={ib.id}
                      onClick={() => handleInboundToggle(ib.id)}
                      style={{
                        cursor: 'pointer',
                        padding: '4px 10px',
                        borderRadius: 6,
                        fontSize: 12,
                        lineHeight: '18px',
                        userSelect: 'none',
                        transition: 'all 0.15s ease',
                        display: 'inline-flex',
                        alignItems: 'center',
                        gap: 5,
                        background: ibChecked ? '#eff6ff' : '#f8fafc',
                        color: ibChecked ? '#2563eb' : '#475569',
                        border: ibChecked ? '1px solid #bfdbfe' : '1px solid #e2e8f0',
                        fontWeight: ibChecked ? 500 : 400,
                      }}
                      title={ib.tag}
                    >
                      <span style={{ fontSize: 11, lineHeight: 1 }}>{ibChecked ? '✓' : '+'}</span>
                      <span>{ib.tag}</span>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
