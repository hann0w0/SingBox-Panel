import { useEffect, useRef, useState } from 'react'
import { formatBytes } from '../../../lib/util'

const ANIMATION_MS = 450

function prefersReducedMotion(): boolean {
  return typeof window !== 'undefined' && typeof window.matchMedia === 'function'
    && window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

/**
 * AnimatedRate smoothly eases a byte-rate value between periodic SSE samples
 * so live telemetry feels fluid instead of snapping abruptly on every tick.
 */
export function AnimatedRate({ value }: { value: number }) {
  const target = Number.isFinite(value) && value > 0 ? value : 0
  const [display, setDisplay] = useState(target)
  const displayRef = useRef(target)

  useEffect(() => {
    const from = displayRef.current
    if (from === target) return
    if (prefersReducedMotion() || typeof requestAnimationFrame !== 'function' || (typeof document !== 'undefined' && document.hidden)) {
      displayRef.current = target
      setDisplay(target)
      return
    }
    let rafId = 0
    const startedAt = performance.now()
    const step = () => {
      const progress = Math.min(1, (performance.now() - startedAt) / ANIMATION_MS)
      // easeOutCubic: quick initial response, gentle settle at the target value
      const eased = 1 - (1 - progress) ** 3
      const current = progress >= 1 ? target : from + (target - from) * eased
      displayRef.current = current
      setDisplay(current)
      if (progress < 1) {
        rafId = requestAnimationFrame(step)
      }
    }
    rafId = requestAnimationFrame(step)
    return () => cancelAnimationFrame(rafId)
  }, [target])

  return <>{`${formatBytes(display)}/s`}</>
}
