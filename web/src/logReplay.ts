// A follower replays the last N lines before emitting new ones. Only discard
// the matching prefix; identical messages later in the stream remain visible.
export class LogReplay {
  private offsets: number[] | null = null

  constructor(private readonly history: string[]) {}

  consume(lines: string[]): string[] {
    if (!this.history.length) return lines
    const result: string[] = []
    for (let i = 0; i < lines.length; i++) {
      const line = lines[i]
      if (this.offsets === null) {
        this.offsets = this.history.flatMap((value, index) => value === line ? [index] : [])
        if (!this.offsets.length) {
          this.history.length = 0
          return [...result, ...lines.slice(i)]
        }
      }
      const matching = this.offsets.filter((offset) => line === this.history[offset])
      if (!matching.length) {
        this.history.length = 0
        result.push(line)
        continue
      }
      this.offsets = matching.map((offset) => offset + 1)
      if (this.offsets.includes(this.history.length)) this.history.length = 0
    }
    return result
  }
}

export function mergeLogSnapshot(history: string[], live: string[]): string[] {
  let overlap = Math.min(history.length, live.length)
  while (overlap > 0 && !history.slice(-overlap).every((line, index) => line === live[index])) overlap--
  return [...history, ...live.slice(overlap)]
}
