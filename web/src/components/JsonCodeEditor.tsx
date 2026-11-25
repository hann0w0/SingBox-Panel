import {
  useCallback,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type UIEvent,
} from 'react'
import { cn } from '@/lib/util'
import { highlightJsonDocument, lineIndexAtOffset } from '../lib/jsonHighlight'

export type JsonCodeEditorProps = {
  value: string
  onChange: (value: string) => void
  ariaLabel?: string
  readOnly?: boolean
  disabled?: boolean
  className?: string
}

const INDENT_UNIT = '  '
const MAX_HISTORY_ENTRIES = 100
const HIGHLIGHT_LINE_LIMIT = 500
const HIGHLIGHT_BYTE_LIMIT = 32768

type EditorSnapshot = { value: string; start: number; end: number }

function unindentPrefixLength(line: string): number {
  if (line.startsWith('\t')) return 1
  if (line.startsWith(INDENT_UNIT)) return INDENT_UNIT.length
  if (line.startsWith(' ')) return 1
  return 0
}

export function JsonCodeEditor({
  value,
  onChange,
  ariaLabel = 'config.json',
  readOnly = false,
  disabled = false,
  className,
}: JsonCodeEditorProps) {
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)
  const gutterInnerRef = useRef<HTMLDivElement | null>(null)
  const highlightRef = useRef<HTMLDivElement | null>(null)
  const pendingSelectionRef = useRef<[number, number] | null>(null)
  const historyRef = useRef<{ entries: EditorSnapshot[]; index: number }>({
    entries: [{ value, start: 0, end: 0 }],
    index: 0,
  })
  const lastEmittedRef = useRef(value)

  const [focused, setFocused] = useState(false)
  const [activeLine, setActiveLine] = useState<number | null>(null)

  const rawLines = useMemo(() => value.split('\n'), [value])
  const isLargeDoc = rawLines.length > HIGHLIGHT_LINE_LIMIT || value.length > HIGHLIGHT_BYTE_LIMIT
  const doc = useMemo(() => (isLargeDoc ? null : highlightJsonDocument(value)), [isLargeDoc, value])
  const lineCount = doc ? doc.lines.length : rawLines.length
  const gutterDigits = Math.max(2, String(lineCount).length)

  const recordHistory = (nextValue: string, selStart: number, selEnd: number, prevStart?: number, prevEnd?: number) => {
    const h = historyRef.current
    if (prevStart !== undefined && h.entries[h.index]) {
      h.entries[h.index] = {
        ...h.entries[h.index],
        start: prevStart,
        end: prevEnd ?? prevStart,
      }
    }
    if (h.entries[h.index]?.value === nextValue) {
      h.entries[h.index] = { value: nextValue, start: selStart, end: selEnd }
      lastEmittedRef.current = nextValue
      return
    }
    const nextEntries = [...h.entries.slice(0, h.index + 1), { value: nextValue, start: selStart, end: selEnd }]
    if (nextEntries.length > MAX_HISTORY_ENTRIES) {
      nextEntries.splice(0, nextEntries.length - MAX_HISTORY_ENTRIES)
    }
    historyRef.current = { entries: nextEntries, index: nextEntries.length - 1 }
    lastEmittedRef.current = nextValue
  }

  const applyScrollTransform = useCallback((top: number, left: number) => {
    if (gutterInnerRef.current) {
      gutterInnerRef.current.style.transform = `translate3d(0, ${-top}px, 0)`
    }
    if (highlightRef.current) {
      highlightRef.current.style.transform = `translate3d(${-left}px, ${-top}px, 0)`
    }
  }, [])

  const handleScroll = (event: UIEvent<HTMLTextAreaElement>) => {
    applyScrollTransform(event.currentTarget.scrollTop, event.currentTarget.scrollLeft)
  }

  const syncActiveLine = useCallback(() => {
    const ta = textareaRef.current
    if (!ta) return
    setActiveLine(lineIndexAtOffset(ta.value, ta.selectionStart ?? 0))
  }, [])

  useLayoutEffect(() => {
    if (value !== lastEmittedRef.current) {
      historyRef.current = { entries: [{ value, start: 0, end: 0 }], index: 0 }
      lastEmittedRef.current = value
    }
    const ta = textareaRef.current
    if (!ta) return
    if (pendingSelectionRef.current) {
      const [start, end] = pendingSelectionRef.current
      pendingSelectionRef.current = null
      ta.setSelectionRange(start, end)
      setActiveLine(lineIndexAtOffset(ta.value, start))
    }
    applyScrollTransform(ta.scrollTop, ta.scrollLeft)
  }, [value, applyScrollTransform])

  const commitEdit = (nextValue: string, selStart: number, selEnd = selStart, prevStart?: number, prevEnd?: number) => {
    recordHistory(nextValue, selStart, selEnd, prevStart, prevEnd)
    pendingSelectionRef.current = [selStart, selEnd]
    onChange(nextValue)
  }

  const handleKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (readOnly || disabled) return
    const ta = event.currentTarget
    const start = ta.selectionStart ?? 0
    const end = ta.selectionEnd ?? start
    const text = ta.value

    const mod = event.metaKey || event.ctrlKey
    if (mod && !event.altKey) {
      const key = event.key.toLowerCase()
      if (key === 'z' || (key === 'y' && !event.shiftKey)) {
        const isRedo = (key === 'z' && event.shiftKey) || key === 'y'
        const h = historyRef.current
        const targetIndex = isRedo ? h.index + 1 : h.index - 1
        event.preventDefault()
        if (targetIndex < 0 || targetIndex >= h.entries.length) return
        const entry = h.entries[targetIndex]
        historyRef.current = { entries: h.entries, index: targetIndex }
        lastEmittedRef.current = entry.value
        pendingSelectionRef.current = [entry.start, entry.end]
        onChange(entry.value)
        return
      }
    }

    if (event.key === 'Tab') {
      event.preventDefault()
      if (!event.shiftKey && start === end) {
        const next = text.slice(0, start) + INDENT_UNIT + text.slice(end)
        commitEdit(next, start + INDENT_UNIT.length, start + INDENT_UNIT.length, start, end)
        return
      }

      const blockStart = text.lastIndexOf('\n', Math.max(0, start - 1)) + 1
      const lineEndBreak = text.indexOf('\n', end)
      const blockEnd = lineEndBreak === -1 ? text.length : lineEndBreak
      const blockLines = text.slice(blockStart, blockEnd).split('\n')

      let nextStart = start
      let nextEnd = end
      let cursor = blockStart
      const updatedLines = blockLines.map((line, index) => {
        const lineLen = line.length
        if (event.shiftKey) {
          const removed = unindentPrefixLength(line)
          if (index === 0) {
            nextStart = Math.max(blockStart, nextStart - Math.min(removed, Math.max(0, start - blockStart)))
          }
          if (end > cursor) {
            nextEnd = Math.max(nextStart, nextEnd - Math.min(removed, Math.max(0, end - cursor)))
          }
          cursor += lineLen + 1
          return line.slice(removed)
        }
        if (index === 0) nextStart += INDENT_UNIT.length
        nextEnd += INDENT_UNIT.length
        cursor += lineLen + 1
        return INDENT_UNIT + line
      })

      const next = text.slice(0, blockStart) + updatedLines.join('\n') + text.slice(blockEnd)
      commitEdit(next, nextStart, nextEnd, start, end)
      return
    }

    if (event.key === 'Enter') {
      event.preventDefault()
      const lineStart = text.lastIndexOf('\n', Math.max(0, start - 1)) + 1
      const beforeCursor = text.slice(lineStart, start)
      const baseIndent = /^[ \t]*/.exec(beforeCursor)?.[0] ?? ''
      const prevChar = beforeCursor.trimEnd().slice(-1)
      const afterCursor = text.slice(end)
      const nextChar = afterCursor.trimStart()[0]

      const opensBlock = prevChar === '{' || prevChar === '['
      const closesBlock = (prevChar === '{' && nextChar === '}') || (prevChar === '[' && nextChar === ']')
      const innerIndent = opensBlock ? baseIndent + INDENT_UNIT : baseIndent

      if (closesBlock) {
        const insert = `\n${innerIndent}\n${baseIndent}`
        const next = text.slice(0, start) + insert + text.slice(end)
        commitEdit(next, start + 1 + innerIndent.length, start + 1 + innerIndent.length, start, end)
        return
      }

      const insert = `\n${innerIndent}`
      const next = text.slice(0, start) + insert + text.slice(end)
      commitEdit(next, start + insert.length, start + insert.length, start, end)
    }
  }

  return (
    <div
      className={cn(
        'fm-code-editor',
        focused && 'is-focused',
        readOnly && 'is-readonly',
        disabled && 'is-disabled',
        className,
      )}
      style={{ ['--fm-gutter-digits' as string]: gutterDigits }}
    >
      <div className="fm-code-gutter" aria-hidden="true">
        <div ref={gutterInnerRef} className="fm-code-gutter-inner">
          {rawLines.map((_, index) => (
            <div
              key={index}
              className={cn('fm-code-gutter-line', activeLine === index && 'is-active')}
            >
              {index + 1}
            </div>
          ))}
        </div>
      </div>

      <div className="fm-code-stage">
        <div ref={highlightRef} className="fm-code-highlight" aria-hidden="true">
          {doc ? doc.lines.map((line, index) => (
            <div
              key={index}
              className={cn('fm-code-line', activeLine === index && 'is-active')}
            >
              {Array.from({ length: line.guideCount }, (_, g) => (
                <span
                  key={g}
                  className="fm-code-guide"
                  style={{ left: `calc(var(--fm-code-pad-x) + ${g * doc.indentStep}ch)` }}
                />
              ))}
              {line.leading}
              {line.leading === '' && line.tokens.length === 0
                ? '\u200b'
                : line.tokens.map((tok, tokenIdx) =>
                    tok.kind === 'plain' ? (
                      tok.text
                    ) : (
                      <span key={tokenIdx} className={`fm-tok-${tok.kind}`}>
                        {tok.text}
                      </span>
                    ),
                  )}
            </div>
          )) : rawLines.map((line, index) => (
            <div
              key={index}
              className={cn('fm-code-line', activeLine === index && 'is-active')}
            >
              {line === '' ? '\u200b' : line}
            </div>
          ))}
        </div>

        <textarea
          ref={textareaRef}
          className="fm-code-input"
          aria-label={ariaLabel}
          spellCheck={false}
          autoCapitalize="off"
          autoComplete="off"
          autoCorrect="off"
          wrap="off"
          value={value}
          readOnly={readOnly}
          disabled={disabled}
          onChange={(event) => {
            const ta = event.currentTarget
            recordHistory(ta.value, ta.selectionStart ?? ta.value.length, ta.selectionEnd ?? ta.value.length)
            onChange(ta.value)
          }}
          onScroll={handleScroll}
          onKeyDown={handleKeyDown}
          onKeyUp={syncActiveLine}
          onClick={syncActiveLine}
          onSelect={syncActiveLine}
          onFocus={() => {
            setFocused(true)
            syncActiveLine()
          }}
          onBlur={() => {
            setFocused(false)
            setActiveLine(null)
          }}
        />
      </div>
    </div>
  )
}
