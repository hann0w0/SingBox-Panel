import { useRef, type KeyboardEvent, type ReactNode } from 'react'

/** Page heading: large title, optional one-line meta row, actions on the right. */
export function PageHeader({ title, meta, actions }: { title: ReactNode; meta?: ReactNode; actions?: ReactNode }) {
  return <header className="ui-page-head">
    <div className="ui-page-title">
      <h1>{title}</h1>
      {meta && <div className="ui-page-meta">{meta}</div>}
    </div>
    {actions && <div className="ui-page-actions">{actions}</div>}
  </header>
}

/** Metadata and actions for a tab whose parent already owns the page title. */
export function PageToolbar({ meta, actions }: { meta?: ReactNode; actions?: ReactNode }) {
  return <div className="ui-page-toolbar">
    <div className="ui-page-meta">{meta}</div>
    <div className="ui-page-actions">{actions}</div>
  </div>
}

/** Bordered strip of headline numbers. */
export function Stats({ items }: { items: { label: ReactNode; value: ReactNode; hint?: ReactNode }[] }) {
  return <div className="ui-stats" style={{ ['--ui-stats-cols' as string]: items.length }}>
    {items.map((item, index) => <div className="ui-stat" key={index}>
      <span>{item.label}</span>
      <b>{item.value}</b>
      {item.hint && <small>{item.hint}</small>}
    </div>)}
  </div>
}

export type PillItem = { key: string; label: ReactNode; count?: number; disabled?: boolean }

/** Segmented pill tabs with an optional action slot on the right. */
export function Pills({ items, active, onChange, actions, label }: {
  items: PillItem[]
  active: string
  onChange: (key: string) => void
  actions?: ReactNode
  label?: string
}) {
  const buttons = useRef(new Map<string, HTMLButtonElement>())
  const focusIndex = Math.max(0, items.findIndex((item) => item.key === active && !item.disabled))
  const moveFocus = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    if (event.altKey || event.ctrlKey || event.metaKey || !items.length) return
    let nextIndex: number
    switch (event.key) {
      case 'ArrowLeft':
        nextIndex = (index - 1 + items.length) % items.length
        break
      case 'ArrowRight':
        nextIndex = (index + 1) % items.length
        break
      case 'Home':
        nextIndex = 0
        break
      case 'End':
        nextIndex = items.length - 1
        break
      default:
        return
    }
    event.preventDefault()
    const next = items[nextIndex]
    buttons.current.get(next.key)?.focus()
    if (next.key !== active) onChange(next.key)
  }
  return <div className="ui-pills-row">
    <div className="ui-pills" role="tablist" aria-label={label} aria-orientation="horizontal">
      {items.map((item, index) => <button key={item.key} type="button" role="tab" aria-selected={item.key === active} disabled={item.disabled}
        ref={(button) => {
          if (button) buttons.current.set(item.key, button)
          else buttons.current.delete(item.key)
        }}
        tabIndex={item.disabled ? -1 : index === focusIndex ? 0 : -1}
        onKeyDown={(event) => moveFocus(event, index)}
        className={`ui-pill${item.key === active ? ' is-active' : ''}${item.disabled ? ' is-disabled' : ''}`} onClick={() => { if (!item.disabled) onChange(item.key) }}>
        {item.label}{item.count !== undefined && <span className="ui-pill-count">{item.count}</span>}
      </button>)}
    </div>
    {actions && <div className="ui-pills-actions">{actions}</div>}
  </div>
}

/** Small coloured dot with a label. */
export function Status({ tone = 'off', children }: { tone?: 'ok' | 'off' | 'warn' | 'error'; children: ReactNode }) {
  return <span className={`ui-status is-${tone}`}><i />{children}</span>
}

/** Plain section with a small heading and optional actions; used inside pages. */
export function Section({ title, actions, children, className = '' }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return <section className={`ui-section ${className}`}>
    {(title || actions) && <header className="ui-section-head">{title && <h2>{title}</h2>}{actions && <div className="ui-section-actions">{actions}</div>}</header>}
    {children}
  </section>
}
