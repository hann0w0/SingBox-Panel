import { Braces, List } from 'lucide-react'

export type EditorMode = 'form' | 'json'

export function EditorModeToggle({
  value,
  onChange,
  ariaLabel,
  disabled = false,
}: {
  value: EditorMode
  onChange: (next: EditorMode) => void
  ariaLabel: string
  disabled?: boolean
}) {
  return (
    <div
      role="radiogroup"
      aria-label={ariaLabel}
      className="fm-mode-toggle"
    >
      <button
        type="button"
        role="radio"
        aria-checked={value === 'form'}
        disabled={disabled}
        onClick={() => onChange('form')}
        className={`fm-mode-toggle-btn ${value === 'form' ? 'is-active' : ''}`}
      >
        <List className="size-3.5 shrink-0" aria-hidden="true" />
        <span>Form</span>
      </button>
      <button
        type="button"
        role="radio"
        aria-checked={value === 'json'}
        disabled={disabled}
        onClick={() => onChange('json')}
        className={`fm-mode-toggle-btn ${value === 'json' ? 'is-active' : ''}`}
      >
        <Braces className="size-3.5 shrink-0" aria-hidden="true" />
        <span>JSON</span>
      </button>
    </div>
  )
}
