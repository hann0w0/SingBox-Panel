import { AlertDialog as AlertPrimitive } from 'radix-ui'
import React, { useEffect, useRef, useState, useSyncExternalStore } from 'react'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogTitle,
  AlertDialogDescription,
} from '@/components/ui/alert-dialog'
import {
  Popover as PopoverRoot,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'
import { Button, type ButtonProps } from './controls'
import { cn } from '@/lib/util'
import { errMsg } from '../../lib/api'

type ModalProps = {
  open?: boolean
  title?: React.ReactNode
  children?: React.ReactNode
  onCancel?: () => void
  onOk?: () => void | Promise<unknown>
  footer?: React.ReactNode
  width?: number
  className?: string
  style?: React.CSSProperties
  okText?: React.ReactNode
  cancelText?: React.ReactNode
  confirmLoading?: boolean
  okButtonProps?: ButtonProps
  cancelButtonProps?: ButtonProps
  maskClosable?: boolean
  closable?: boolean
  keyboard?: boolean
}
function ModalRoot({
  open,
  title,
  children,
  onCancel,
  onOk,
  footer,
  width = 480,
  className,
  style,
  okText = '确定',
  cancelText = '取消',
  confirmLoading,
  okButtonProps,
  cancelButtonProps,
  maskClosable = true,
  closable = true,
  keyboard = true,
}: ModalProps) {
  const opener = useRef<HTMLElement | null>(null)
  const locked = useRef(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const busy = confirmLoading || pending
  useEffect(() => { if (open) setError('') }, [open])
  const cancel = () => {
    if (!busy && !locked.current) onCancel?.()
  }
  const submit = async () => {
    if (busy || locked.current) return
    locked.current = true
    setPending(true)
    setError('')
    try {
      await onOk?.()
    } catch (cause: any) {
      // Field validation already displays its errors next to the controls.
      if (!cause?.errorFields) setError(errMsg(cause))
    } finally {
      locked.current = false
      setPending(false)
    }
  }
  return (
    <Dialog
      open={!!open}
      onOpenChange={(next) => {
        if (!next) cancel()
      }}
    >
      <DialogContent
        className={cn('ui-modal', className)}
        style={{ width, ...style }}
        showCloseButton={closable && !busy}
        aria-describedby={undefined}
        onOpenAutoFocus={() => {
          opener.current = document.activeElement as HTMLElement
        }}
        onCloseAutoFocus={(event) => {
          event.preventDefault()
          opener.current?.focus()
        }}
        onEscapeKeyDown={(event) => {
          if (!keyboard || busy) event.preventDefault()
        }}
        onPointerDownOutside={(event) => {
          if (!maskClosable || busy) event.preventDefault()
        }}
      >
        <header className="ui-modal-header">
          <DialogTitle className="ui-modal-title">
            {title || <span className="sr-only">对话框</span>}
          </DialogTitle>
        </header>
        <div className="ui-modal-body">
          {children}
          {error && <p className="text-destructive mt-3" role="alert">{error}</p>}
        </div>
        {footer !== null && (
          <footer className="ui-modal-footer">
            {footer === undefined ? (
              <>
                <Button {...cancelButtonProps} disabled={busy || cancelButtonProps?.disabled} onClick={cancel}>
                  {cancelText}
                </Button>
                <Button
                  type="primary"
                  {...okButtonProps}
                  loading={busy}
                  onClick={() => void submit()}
                >
                  {okText}
                </Button>
              </>
            ) : (
              footer
            )}
          </footer>
        )}
      </DialogContent>
    </Dialog>
  )
}
type ConfirmProps = {
  title?: React.ReactNode
  content?: React.ReactNode
  okText?: React.ReactNode
  cancelText?: React.ReactNode
  okType?: string
  okButtonProps?: ButtonProps
  onOk?: () => unknown
  onCancel?: () => unknown
  width?: number
}
type Entry = ConfirmProps & {
  id: number
  kind: string
  opener: HTMLElement | null
}
let entries: Entry[] = []
let nextId = 0
const listeners = new Set<() => void>()
const subscribe = (listener: () => void) => {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}
const snapshot = () => entries
function emit() {
  listeners.forEach((listener) => listener())
}
function remove(id: number) {
  const entry = entries.find((item) => item.id === id)
  entries = entries.filter((item) => item.id !== id)
  emit()
  requestAnimationFrame(() => entry?.opener?.focus())
}
function confirm(kind: string, props: ConfirmProps) {
  const id = ++nextId
  entries = [
    ...entries,
    { ...props, id, kind, opener: document.activeElement as HTMLElement },
  ]
  emit()
  return {
    destroy: () => remove(id),
    update: (value: Partial<ConfirmProps>) => {
      entries = entries.map((item) =>
        item.id === id ? { ...item, ...value } : item,
      )
      emit()
    },
  }
}
function Confirmation({ entry }: { entry: Entry }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const locked = useRef(false)
  const confirmButton = useRef<HTMLButtonElement>(null)
  const run = async () => {
    if (locked.current) return
    locked.current = true
    setBusy(true)
    setError('')
    try {
      await entry.onOk?.()
      remove(entry.id)
    } catch (e) {
      setError(errMsg(e))
    } finally {
      locked.current = false
      setBusy(false)
    }
  }
  const cancel = () => {
    if (busy) return
    entry.onCancel?.()
    remove(entry.id)
  }
  return (
    <AlertDialog
      open
      onOpenChange={(next) => {
        if (!next) cancel()
      }}
    >
      <AlertDialogContent
        className="ui-modal ui-confirm"
        style={{ width: entry.width || 416 }}
        onOpenAutoFocus={(event) => {
          if (entry.kind !== 'confirm') {
            event.preventDefault()
            confirmButton.current?.focus({ preventScroll: true })
          }
        }}
        onEscapeKeyDown={(event) => {
          if (busy) event.preventDefault()
        }}
      >
        <header className="ui-modal-header">
          <AlertDialogTitle className="ui-modal-title">
            {entry.title || '提示'}
          </AlertDialogTitle>
        </header>
        <AlertDialogDescription asChild>
          <div
            className={cn(
              'ui-modal-body ui-confirm-description',
              !entry.content && !error && 'sr-only',
            )}
          >
            {entry.content || '请确认操作'}
            {error && (
              <p className="text-destructive mt-3" role="alert">
                {error}
              </p>
            )}
          </div>
        </AlertDialogDescription>
        <footer className="ui-modal-footer">
          {entry.kind === 'confirm' && (
            <AlertPrimitive.Cancel asChild>
              <Button disabled={busy}>{entry.cancelText || '取消'}</Button>
            </AlertPrimitive.Cancel>
          )}
          <Button
            ref={confirmButton}
            type="primary"
            danger={entry.okType === 'danger' || entry.okButtonProps?.danger}
            loading={busy}
            onClick={() => void run()}
            {...entry.okButtonProps}
          >
            {entry.okText || (entry.kind === 'confirm' ? '确定' : '知道了')}
          </Button>
        </footer>
      </AlertDialogContent>
    </AlertDialog>
  )
}
export function DialogHost() {
  const current = useSyncExternalStore(subscribe, snapshot, snapshot)
  return (
    <>
      {current.map((entry) => (
        <Confirmation key={entry.id} entry={entry} />
      ))}
    </>
  )
}
export const Modal = Object.assign(ModalRoot, {
  confirm: (props: ConfirmProps) => confirm('confirm', props),
  info: (props: ConfirmProps) => confirm('info', props),
  success: (props: ConfirmProps) => confirm('success', props),
  warning: (props: ConfirmProps) => confirm('warning', props),
  error: (props: ConfirmProps) => confirm('error', props),
  destroyAll: () => {
    entries = []
    emit()
  },
})
export function Popover({
  children,
  content,
  title,
  open,
  onOpenChange,
}: {
  children: React.ReactNode
  content?: React.ReactNode
  title?: React.ReactNode
  open?: boolean
  onOpenChange?: (open: boolean) => void
}) {
  return (
    <PopoverRoot open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger asChild>{children}</PopoverTrigger>
      <PopoverContent className="ui-popover">
        {title && <div className="ui-popover-title">{title}</div>}
        {content}
      </PopoverContent>
    </PopoverRoot>
  )
}
export function Popconfirm({
  children,
  title,
  description,
  okText = '确定',
  cancelText = '取消',
  onConfirm,
  okButtonProps,
}: {
  children: React.ReactNode
  title: React.ReactNode
  description?: React.ReactNode
  okText?: string
  cancelText?: string
  onConfirm?: () => unknown
  okButtonProps?: ButtonProps
}) {
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const locked = useRef(false)
  return (
    <PopoverRoot
      open={open}
      onOpenChange={(value) => {
        if (!locked.current) {
          setOpen(value)
          if (value) setError('')
        }
      }}
    >
      <PopoverTrigger asChild>{children}</PopoverTrigger>
      <PopoverContent className="ui-popconfirm" align="end">
        <div className="ui-popconfirm-message">
          <div>{title}</div>
          {description && <p>{description}</p>}
          {error && <p className="text-destructive mt-3" role="alert">{error}</p>}
        </div>
        <div className="ui-popconfirm-buttons">
          <Button size="small" disabled={busy} onClick={() => setOpen(false)}>
            {cancelText}
          </Button>
          <Button
            size="small"
            type="primary"
            {...okButtonProps}
            loading={busy}
            onClick={async () => {
              if (locked.current) return
              locked.current = true
              setBusy(true)
              setError('')
              try {
                await onConfirm?.()
                setOpen(false)
              } catch (cause) {
                setError(errMsg(cause))
              } finally {
                locked.current = false
                setBusy(false)
              }
            }}
          >
            {okText}
          </Button>
        </div>
      </PopoverContent>
    </PopoverRoot>
  )
}
export type MenuItem = {
  key?: React.Key
  type?: 'divider'
  label?: React.ReactNode
  icon?: React.ReactNode
  disabled?: boolean
  danger?: boolean
  onClick?: () => void
}
export type MenuProps = {
  items?: (MenuItem | null)[]
  onClick?: (info: { key: string }) => void
  selectable?: boolean
  selectedKeys?: string[]
}
export function Dropdown({
  children,
  menu,
  placement,
  onOpenChange,
}: {
  children: React.ReactNode
  menu: MenuProps
  placement?: string
  onOpenChange?: (open: boolean) => void
}) {
  return (
    <DropdownMenu onOpenChange={onOpenChange}>
      <DropdownMenuTrigger asChild>{children}</DropdownMenuTrigger>
      <DropdownMenuContent
        className="ui-dropdown ui-dropdown-menu"
        align={placement?.includes('Right') ? 'end' : 'start'}
      >
        {menu.items?.map((item, index) =>
          !item ? null : item.type === 'divider' ? (
            <DropdownMenuSeparator key={index} />
          ) : (
            <DropdownMenuItem
              key={item.key ?? index}
              disabled={item.disabled}
              variant={item.danger ? 'destructive' : 'default'}
              onSelect={() => {
                item.onClick?.()
                menu.onClick?.({ key: String(item.key) })
              }}
            >
              {item.icon}
              {item.label}
              {menu.selectedKeys?.includes(String(item.key)) && (
                <span className="ml-auto">✓</span>
              )}
            </DropdownMenuItem>
          ),
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
