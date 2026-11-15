import React, { useMemo, useRef, useState } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { ArrowDown, ArrowUp, ChevronsUpDown } from 'lucide-react'
import {
  TableHeader,
  TableBody,
  TableRow,
  TableHead,
  TableCell,
} from '@/components/ui/table'
import { cn } from '@/lib/utils'
import { useBreakpoint } from './controls'
type Column<T> = {
  key?: React.Key
  title?: React.ReactNode
  dataIndex?: string | string[]
  width?: number | string
  align?: 'left' | 'center' | 'right'
  ellipsis?: boolean
  render?: (value: any, record: T, index: number) => React.ReactNode
  sorter?: ((a: T, b: T) => number) | boolean
  defaultSortOrder?: 'ascend' | 'descend'
  responsive?: string[]
  className?: string
  fixed?: 'left' | 'right' | boolean
}
export type ColumnsType<T = any> = Column<T>[]
type TableProps<T> = {
  columns?: ColumnsType<T>
  dataSource?: T[]
  rowKey?: string | ((row: T, index?: number) => React.Key)
  rowClassName?: string | ((row: T, index: number) => string)
  onRow?: (row: T, index?: number) => React.HTMLAttributes<HTMLTableRowElement>
  size?: string
  className?: string
  style?: React.CSSProperties
  scroll?: { x?: number | string; y?: number | string }
  virtual?: boolean
  tableLayout?: 'auto' | 'fixed'
  locale?: { emptyText?: React.ReactNode }
  summary?: (data: T[]) => React.ReactNode
}
function TableRoot<T extends object>({
  columns = [],
  dataSource = [],
  rowKey = 'key',
  rowClassName,
  onRow,
  className,
  size,
  style,
  scroll,
  virtual,
  tableLayout,
  locale,
  summary,
}: TableProps<T>) {
  const breakpoint = useBreakpoint()
  const visible = columns.filter(
    (column) =>
      !column.responsive ||
      column.responsive.some(
        (size) => breakpoint[size as keyof typeof breakpoint],
      ),
  )
  const defaultIndex = columns.findIndex((column) => column.defaultSortOrder)
  const [sorting, setSorting] = useState<{
    index: number
    order: 'ascend' | 'descend'
  } | null>(
    defaultIndex >= 0
      ? { index: defaultIndex, order: columns[defaultIndex].defaultSortOrder! }
      : null,
  )
  const rows = useMemo(() => {
    if (!sorting) return dataSource
    const compare = columns[sorting.index]?.sorter
    return typeof compare === 'function'
      ? [...dataSource].sort(
          (a, b) => compare(a, b) * (sorting.order === 'ascend' ? 1 : -1),
        )
      : dataSource
  }, [dataSource, columns, sorting])
  const viewport = useRef<HTMLDivElement>(null)
  const getKey = (row: T, index: number) =>
    typeof rowKey === 'function'
      ? rowKey(row, index)
      : ((row as any)[rowKey] ?? index)
  const windowed = Boolean(virtual && scroll?.y)
  const virtualizer = useVirtualizer({
    count: windowed ? rows.length : 0,
    getScrollElement: () => viewport.current,
    estimateSize: () => size === 'small' ? 40 : 48,
    overscan: 8,
    getItemKey: (index) => getKey(rows[index], index),
  })
  const items = windowed ? virtualizer.getVirtualItems() : []
  const row = (record: T, index: number) => {
    const rowProps = onRow?.(record, index)
    return (
      <TableRow
        {...rowProps}
        key={getKey(record, index)}
        data-index={index}
        data-row-key={getKey(record, index)}
        ref={windowed ? virtualizer.measureElement : undefined}
        className={cn(
          typeof rowClassName === 'function'
            ? rowClassName(record, index)
            : rowClassName,
          rowProps?.className,
          'ui-table-row',
        )}
      >
        {visible.map((column, i) => {
          const value = column.dataIndex
            ? (Array.isArray(column.dataIndex)
                ? column.dataIndex
                : [column.dataIndex]
              ).reduce((v, key) => v?.[key], record as any)
            : undefined
          return (
            <TableCell
              key={column.key ?? i}
              className={cn(
                'ui-table-cell',
                column.ellipsis && 'ui-table-cell-ellipsis',
                column.className,
              )}
              style={{
                textAlign: column.align,
                ...(column.fixed
                  ? {
                      position: 'sticky',
                      right: column.fixed === 'right' ? 0 : undefined,
                      left: column.fixed === 'left' ? 0 : undefined,
                      background: 'var(--console-container)',
                      zIndex: 1,
                    }
                  : {}),
              }}
            >
              {column.render
                ? column.render(value, record, index)
                : (value as React.ReactNode)}
            </TableCell>
          )
        })}
      </TableRow>
    )
  }
  return (
    <div className={cn('ui-table-wrapper', size === 'small' && 'ui-table-sm', className)} style={style}>
      <div
        className="ui-table-container"
        ref={viewport}
        style={{ maxHeight: scroll?.y, overflow: 'auto' }}
      >
        <table
          data-slot="table"
          className="ui-table w-full caption-bottom text-sm"
          style={{ minWidth: scroll?.x, tableLayout }}
        >
          <colgroup>
            {visible.map((column, i) => (
              <col key={i} style={{ width: column.width }} />
            ))}
          </colgroup>
          <TableHeader className="ui-table-thead">
            <TableRow>
              {visible.map((column, i) => {
                const index = columns.indexOf(column)
                return (
                  <TableHead
                    className="ui-table-cell"
                    key={column.key ?? i}
                    style={{
                      textAlign: column.align,
                      ...(column.fixed
                        ? {
                            position: 'sticky',
                            right: column.fixed === 'right' ? 0 : undefined,
                            left: column.fixed === 'left' ? 0 : undefined,
                            background: 'var(--console-surface)',
                            zIndex: 2,
                          }
                        : {}),
                    }}
                  >
                    {column.sorter ? (
                      <button
                        type="button"
                        className="ui-table-sort"
                        onClick={() =>
                          setSorting((current) =>
                            current?.index !== index
                              ? { index, order: 'ascend' }
                              : current.order === 'ascend'
                                ? { index, order: 'descend' }
                                : null,
                          )
                        }
                        aria-label={`排序 ${typeof column.title === 'string' ? column.title : ''}`}
                      >
                        {column.title}
                        {sorting?.index === index ? (
                          sorting.order === 'ascend' ? (
                            <ArrowUp size={12} />
                          ) : (
                            <ArrowDown size={12} />
                          )
                        ) : (
                          <ChevronsUpDown size={12} />
                        )}
                      </button>
                    ) : (
                      column.title
                    )}
                  </TableHead>
                )
              })}
            </TableRow>
          </TableHeader>
          <TableBody className="ui-table-tbody">
            {!rows.length ? (
              <TableRow>
                <TableCell colSpan={visible.length} className="ui-table-empty">
                  {locale?.emptyText || '暂无数据'}
                </TableCell>
              </TableRow>
            ) : windowed ? (
              <>
                {items.length > 0 && items[0].start > 0 && (
                  <tr aria-hidden>
                    <td
                      colSpan={visible.length}
                      style={{ height: items[0].start, padding: 0, border: 0 }}
                    />
                  </tr>
                )}
                {items.map((item) => row(rows[item.index], item.index))}
                {items.length > 0 && (
                  <tr aria-hidden>
                    <td
                      colSpan={visible.length}
                      style={{
                        height: Math.max(
                          0,
                          virtualizer.getTotalSize() -
                            items[items.length - 1].end,
                        ),
                        padding: 0,
                        border: 0,
                      }}
                    />
                  </tr>
                )}
              </>
            ) : (
              rows.map(row)
            )}
          </TableBody>
          {summary?.(rows)}
        </table>
      </div>
    </div>
  )
}
function SummaryRoot({
  children,
}: {
  children?: React.ReactNode
  fixed?: boolean
}) {
  return <tfoot className="ui-table-summary">{children}</tfoot>
}
const Summary = Object.assign(SummaryRoot, {
  Row: ({ children, ...props }: React.HTMLAttributes<HTMLTableRowElement>) => (
    <tr {...props}>{children}</tr>
  ),
  Cell: ({
    index,
    children,
    ...props
  }: React.TdHTMLAttributes<HTMLTableCellElement> & { index: number }) => (
    <td {...props} className="ui-table-cell">
      {children}
    </td>
  ),
})
export const Table = Object.assign(TableRoot, { Summary })
