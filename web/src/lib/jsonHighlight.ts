export type JsonTokenKind =
  | 'plain'
  | 'key'
  | 'string'
  | 'number'
  | 'keyword'
  | 'bracket'
  | 'punct'
  | 'comment'
  | 'invalid'

export type JsonToken = {
  kind: JsonTokenKind
  text: string
}

export type HighlightedJsonLine = {
  leading: string
  guideCount: number
  tokens: JsonToken[]
}

export type HighlightedJsonDocument = {
  indentStep: number
  lines: HighlightedJsonLine[]
}

const TAB_WIDTH = 2
const IDENT_RE = /[A-Za-z0-9_$]/
const NUMBER_RE = /^-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/

function countLeadingWhitespace(line: string): { leading: string; cols: number } {
  let i = 0
  let cols = 0
  while (i < line.length) {
    const ch = line[i]
    if (ch === ' ') {
      cols += 1
      i += 1
    } else if (ch === '\t') {
      cols += TAB_WIDTH
      i += 1
    } else {
      break
    }
  }
  return { leading: line.slice(0, i), cols }
}

/** Infer whether the document uses 2-space or 4-space indentation. */
export function detectIndentStep(lines: string[]): number {
  let hasFour = false
  for (const line of lines) {
    const { leading, cols } = countLeadingWhitespace(line)
    if (cols <= 0 || leading.length === line.length) continue
    if (cols % 4 === 2) return 2
    if (cols % 4 === 0) hasFour = true
  }
  return hasFour ? 4 : 2
}

function pushToken(tokens: JsonToken[], kind: JsonTokenKind, text: string) {
  if (!text) return
  const prev = tokens[tokens.length - 1]
  if (prev && prev.kind === kind) {
    prev.text += text
    return
  }
  tokens.push({ kind, text })
}

/** Tokenize a single JSON/JSONC line for syntax highlighting and indent guides. */
export function tokenizeJsonLine(line: string, indentStep = 2): HighlightedJsonLine {
  const { leading, cols } = countLeadingWhitespace(line)
  const guideCount = indentStep > 0 ? Math.floor(cols / indentStep) : 0
  const rest = line.slice(leading.length)
  const tokens: JsonToken[] = []

  let i = 0
  while (i < rest.length) {
    const ch = rest[i]

    if (ch === ' ' || ch === '\t') {
      let end = i + 1
      while (end < rest.length && (rest[end] === ' ' || rest[end] === '\t')) end += 1
      pushToken(tokens, 'plain', rest.slice(i, end))
      i = end
      continue
    }

    if (ch === '/' && rest[i + 1] === '/') {
      pushToken(tokens, 'comment', rest.slice(i))
      break
    }

    if (ch === '"') {
      let end = i + 1
      let closed = false
      while (end < rest.length) {
        if (rest[end] === '\\') {
          end += 2
          continue
        }
        if (rest[end] === '"') {
          end += 1
          closed = true
          break
        }
        end += 1
      }
      let lookahead = end
      while (lookahead < rest.length && (rest[lookahead] === ' ' || rest[lookahead] === '\t')) {
        lookahead += 1
      }
      const isKey = closed && rest[lookahead] === ':'
      pushToken(tokens, isKey ? 'key' : 'string', rest.slice(i, end))
      i = end
      continue
    }

    if (ch === '{' || ch === '}' || ch === '[' || ch === ']') {
      pushToken(tokens, 'bracket', ch)
      i += 1
      continue
    }

    if (ch === ':' || ch === ',') {
      pushToken(tokens, 'punct', ch)
      i += 1
      continue
    }

    const prevBounded = i === 0 || !IDENT_RE.test(rest[i - 1])
    if (prevBounded) {
      let kw: string | null = null
      if (rest.startsWith('true', i)) kw = 'true'
      else if (rest.startsWith('false', i)) kw = 'false'
      else if (rest.startsWith('null', i)) kw = 'null'
      if (kw) {
        const after = rest[i + kw.length]
        if (!after || !IDENT_RE.test(after)) {
          pushToken(tokens, 'keyword', kw)
          i += kw.length
          continue
        }
      }

      if (ch === '-' || (ch >= '0' && ch <= '9')) {
        const match = NUMBER_RE.exec(rest.slice(i))
        if (match) {
          const after = rest[i + match[0].length]
          if (!after || !IDENT_RE.test(after)) {
            pushToken(tokens, 'number', match[0])
            i += match[0].length
            continue
          }
        }
      }
    }

    pushToken(tokens, 'invalid', ch)
    i += 1
  }

  return { leading, guideCount, tokens }
}

/** Highlight a full JSON document line-by-line with detected indent guides. */
export function highlightJsonDocument(text: string): HighlightedJsonDocument {
  const rawLines = text.replace(/\r\n?/g, '\n').split('\n')
  const indentStep = detectIndentStep(rawLines)
  return {
    indentStep,
    lines: rawLines.map((line) => tokenizeJsonLine(line, indentStep)),
  }
}

/** Compute 0-based line index for a character offset in text. */
export function lineIndexAtOffset(text: string, offset: number): number {
  const clamped = Math.max(0, Math.min(offset, text.length))
  let line = 0
  for (let i = 0; i < clamped; i += 1) {
    if (text.charCodeAt(i) === 10) line += 1
  }
  return line
}
