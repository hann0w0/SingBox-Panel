import {
  forwardRef,
  useCallback,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type ForwardedRef,
  type Key,
  type ReactElement,
  type ReactNode,
  type UIEvent,
} from 'react'

interface VirtualListProps<T> {
  items: T[]
  getKey: (item: T) => Key
  renderItem: (item: T, index: number) => ReactNode
  estimatedItemHeight: number
  overscan?: number
  gap?: number
  className?: string
  style?: CSSProperties
  role?: string
  ariaLabel?: string
  onScroll?: (event: UIEvent<HTMLDivElement>) => void
}

function MeasuredRow({
  itemKey,
  top,
  gap,
  onHeight,
  children,
}: {
  itemKey: Key
  top: number
  gap: number
  onHeight: (key: Key, height: number) => void
  children: ReactNode
}) {
  const ref = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => {
    const element = ref.current
    if (!element) return
    const measure = () => onHeight(itemKey, element.getBoundingClientRect().height)
    measure()
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure)
    observer?.observe(element)
    return () => observer?.disconnect()
  }, [itemKey, onHeight])

  return (
    <div
      ref={ref}
      style={{ position: 'absolute', top, left: 0, minWidth: '100%', boxSizing: 'border-box', paddingBottom: gap }}
    >
      {children}
    </div>
  )
}

function VirtualListInner<T>(
  {
    items,
    getKey,
    renderItem,
    estimatedItemHeight,
    overscan = 6,
    gap = 0,
    className,
    style,
    role,
    ariaLabel,
    onScroll,
  }: VirtualListProps<T>,
  forwardedRef: ForwardedRef<HTMLDivElement>,
) {
  const outerRef = useRef<HTMLDivElement>(null)
  const heightsRef = useRef(new Map<Key, number>())
  const [heightRevision, setHeightRevision] = useState(0)
  const [viewportHeight, setViewportHeight] = useState(0)
  const [scrollTop, setScrollTop] = useState(0)

  // Keep this callback stable while scrolling. An inline callback would change
  // on every scroll render, forcing every visible row to tear down and recreate
  // its ResizeObserver — exactly the kind of churn that causes mobile jank.
  const handleMeasuredHeight = useCallback((key: Key, height: number) => {
    if (Math.abs((heightsRef.current.get(key) ?? 0) - height) < 0.5) return
    heightsRef.current.set(key, height)
    setHeightRevision((value) => value + 1)
  }, [])

  useImperativeHandle(forwardedRef, () => outerRef.current as HTMLDivElement)

  useLayoutEffect(() => {
    const element = outerRef.current
    if (!element) return
    const measure = () => setViewportHeight(element.clientHeight)
    measure()
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure)
    observer?.observe(element)
    return () => observer?.disconnect()
  }, [])

  const layout = useMemo(() => {
    const keys = items.map(getKey)
    const liveKeys = new Set(keys)
    for (const key of heightsRef.current.keys()) {
      if (!liveKeys.has(key)) heightsRef.current.delete(key)
    }
    const offsets = new Array<number>(items.length)
    const heights = new Array<number>(items.length)
    let total = 0
    for (let index = 0; index < items.length; index += 1) {
      offsets[index] = total
      const height = heightsRef.current.get(keys[index]) ?? estimatedItemHeight + gap
      heights[index] = height
      total += height
    }
    return { keys, offsets, heights, total }
  }, [estimatedItemHeight, gap, getKey, heightRevision, items])

  const startIndex = useMemo(() => {
    if (!items.length) return 0
    const target = Math.max(0, scrollTop)
    let low = 0
    let high = items.length - 1
    while (low < high) {
      const mid = Math.floor((low + high) / 2)
      if (layout.offsets[mid] + layout.heights[mid] < target) low = mid + 1
      else high = mid
    }
    return Math.max(0, low - overscan)
  }, [items.length, layout.heights, layout.offsets, overscan, scrollTop])

  const endIndex = useMemo(() => {
    if (!items.length) return 0
    const bottom = scrollTop + Math.max(viewportHeight, estimatedItemHeight * 8)
    let index = startIndex
    while (index < items.length && layout.offsets[index] < bottom) index += 1
    return Math.min(items.length, index + overscan)
  }, [estimatedItemHeight, items.length, layout.offsets, overscan, scrollTop, startIndex, viewportHeight])

  const handleScroll = (event: UIEvent<HTMLDivElement>) => {
    setScrollTop(event.currentTarget.scrollTop)
    onScroll?.(event)
  }

  return (
    <div
      ref={outerRef}
      className={className}
      style={{ overflow: 'auto', ...style }}
      role={role}
      aria-label={ariaLabel}
      onScroll={handleScroll}
    >
      <div style={{ position: 'relative', height: layout.total, minWidth: '100%' }}>
        {items.slice(startIndex, endIndex).map((item, relativeIndex) => {
          const index = startIndex + relativeIndex
          const key = layout.keys[index]
          return (
            <MeasuredRow
              key={key}
              itemKey={key}
              top={layout.offsets[index]}
              gap={gap}
              onHeight={handleMeasuredHeight}
            >
              {renderItem(item, index)}
            </MeasuredRow>
          )
        })}
      </div>
    </div>
  )
}

export const VirtualList = forwardRef(VirtualListInner) as <T>(
  props: VirtualListProps<T> & { ref?: ForwardedRef<HTMLDivElement> },
) => ReactElement
