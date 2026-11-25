import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { afterEach, describe, expect, it } from 'vitest'
import { highlightJsonDocument, tokenizeJsonLine } from '../lib/jsonHighlight'
import { JsonCodeEditor } from './JsonCodeEditor'

afterEach(cleanup)

const SAMPLE_CONFIG = `{
  "log": {
    "level": "info",
    "timestamp": true
  },
  "inbounds": [
    {
      "listen": "::",
      "listen_port": 443,
      "tag": "vless-a51bf3"
    }
  ]
}`

function ControlledEditor({ initial = SAMPLE_CONFIG, readOnly = false }: { initial?: string; readOnly?: boolean }) {
  const [value, setValue] = useState(initial)
  return <JsonCodeEditor value={value} onChange={setValue} readOnly={readOnly} />
}

describe('jsonHighlight', () => {
  it('tokenizes keys, strings, numbers, booleans, brackets, and punctuation', () => {
    const line = tokenizeJsonLine('      "listen_port": 443, "enabled": true')
    expect(line.leading).toBe('      ')
    expect(line.guideCount).toBe(3)
    expect(line.tokens).toEqual([
      { kind: 'key', text: '"listen_port"' },
      { kind: 'punct', text: ':' },
      { kind: 'plain', text: ' ' },
      { kind: 'number', text: '443' },
      { kind: 'punct', text: ',' },
      { kind: 'plain', text: ' ' },
      { kind: 'key', text: '"enabled"' },
      { kind: 'punct', text: ':' },
      { kind: 'plain', text: ' ' },
      { kind: 'keyword', text: 'true' },
    ])
  })

  it('keeps strings with colons as string values when not followed by a colon', () => {
    const line = tokenizeJsonLine('      "listen": "::",')
    expect(line.tokens).toEqual([
      { kind: 'key', text: '"listen"' },
      { kind: 'punct', text: ':' },
      { kind: 'plain', text: ' ' },
      { kind: 'string', text: '"::"' },
      { kind: 'punct', text: ',' },
    ])
  })

  it('infers 4-space indentation when appropriate and 2-space by default', () => {
    const twoSpace = highlightJsonDocument('{\n  "a": 1\n}')
    expect(twoSpace.indentStep).toBe(2)
    expect(twoSpace.lines[1].guideCount).toBe(1)

    const fourSpace = highlightJsonDocument('{\n    "a": {\n        "b": 1\n    }\n}')
    expect(fourSpace.indentStep).toBe(4)
    expect(fourSpace.lines[1].guideCount).toBe(1)
    expect(fourSpace.lines[2].guideCount).toBe(2)
  })
})

describe('JsonCodeEditor', () => {
  it('renders line numbers, syntax token classes, and indent guides', () => {
    const { container } = render(<ControlledEditor />)

    const gutterLines = container.querySelectorAll('.fm-code-gutter-line')
    expect(gutterLines).toHaveLength(13)
    expect(gutterLines[0].textContent).toBe('1')
    expect(gutterLines[12].textContent).toBe('13')

    expect(container.querySelectorAll('.fm-tok-key').length).toBeGreaterThan(0)
    expect(container.querySelectorAll('.fm-tok-string').length).toBeGreaterThan(0)
    expect(container.querySelectorAll('.fm-tok-number').length).toBe(1)
    expect(container.querySelectorAll('.fm-tok-keyword').length).toBe(1)

    const codeLines = container.querySelectorAll('.fm-code-line')
    expect(codeLines[0].querySelectorAll('.fm-code-guide')).toHaveLength(0)
    expect(codeLines[1].querySelectorAll('.fm-code-guide')).toHaveLength(1)
    expect(codeLines[2].querySelectorAll('.fm-code-guide')).toHaveLength(2)
    expect(codeLines[7].querySelectorAll('.fm-code-guide')).toHaveLength(3)
  })

  it('supports Tab, Shift+Tab, and Enter auto-indentation when editable', () => {
    render(<ControlledEditor initial={'{\n  "a": 1\n}'} />)
    const textarea = screen.getByRole('textbox', { name: 'config.json' }) as HTMLTextAreaElement

    textarea.setSelectionRange(1, 1)
    fireEvent.keyDown(textarea, { key: 'Enter' })
    expect(textarea.value).toBe('{\n  \n  "a": 1\n}')

    fireEvent.keyDown(textarea, { key: 'Tab' })
    expect(textarea.value).toBe('{\n    \n  "a": 1\n}')

    fireEvent.keyDown(textarea, { key: 'Tab', shiftKey: true })
    expect(textarea.value).toBe('{\n  \n  "a": 1\n}')
  })

  it('does not mutate text on Tab or Enter when readOnly', () => {
    render(<ControlledEditor initial={'{\n  "a": 1\n}'} readOnly />)
    const textarea = screen.getByRole('textbox', { name: 'config.json' }) as HTMLTextAreaElement
    expect(textarea.readOnly).toBe(true)

    textarea.setSelectionRange(1, 1)
    fireEvent.keyDown(textarea, { key: 'Tab' })
    fireEvent.keyDown(textarea, { key: 'Enter' })
    expect(textarea.value).toBe('{\n  "a": 1\n}')
  })

  it('supports Ctrl+Z undo and Ctrl+Shift+Z redo after Tab and typing while restoring selection', () => {
    render(<ControlledEditor initial={'{\n  "a": 1\n}'} />)
    const textarea = screen.getByRole('textbox', { name: 'config.json' }) as HTMLTextAreaElement

    textarea.setSelectionRange(2, 2)
    fireEvent.keyDown(textarea, { key: 'Tab' })
    expect(textarea.value).toBe('{\n    "a": 1\n}')
    expect(textarea.selectionStart).toBe(4)

    fireEvent.change(textarea, { target: { value: '{\n  "b": 2,\n    "a": 1\n}', selectionStart: 10, selectionEnd: 10 } })
    fireEvent.keyDown(textarea, { key: 'z', ctrlKey: true })
    expect(textarea.value).toBe('{\n    "a": 1\n}')
    expect(textarea.selectionStart).toBe(4)

    fireEvent.keyDown(textarea, { key: 'z', ctrlKey: true })
    expect(textarea.value).toBe('{\n  "a": 1\n}')
    expect(textarea.selectionStart).toBe(2)

    fireEvent.keyDown(textarea, { key: 'z', ctrlKey: true, shiftKey: true })
    expect(textarea.value).toBe('{\n    "a": 1\n}')
    expect(textarea.selectionStart).toBe(4)
  })

  it('uses plain line rendering without token spans on large documents over 500 lines', () => {
    const large = `{\n${Array.from({ length: 520 }, (_, i) => `  "k${i}": ${i}`).join(',\n')}\n}`
    const { container } = render(<ControlledEditor initial={large} />)
    expect(container.querySelectorAll('.fm-code-gutter-line').length).toBe(522)
    expect(container.querySelectorAll('.fm-tok-key').length).toBe(0)
    expect(container.querySelectorAll('.fm-code-guide').length).toBe(0)
  })
})
