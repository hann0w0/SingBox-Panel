import React from 'react'
import { toast, Toaster } from 'sonner'
type Message =
  | React.ReactNode
  | { content: React.ReactNode; key?: string; duration?: number }
const show =
  (type: 'success' | 'error' | 'info' | 'warning' | 'loading') =>
  (input: Message, duration?: number) => {
    const config =
      typeof input === 'object' && input !== null && 'content' in input
        ? input
        : { content: input, duration }
    const seconds = config.duration ?? (type === 'loading' ? 0 : 3)
    const id = toast[type](config.content, {
      id: config.key,
      duration: seconds === 0 ? Infinity : seconds * 1000,
    })
    return () => toast.dismiss(id)
  }
export const message = {
  success: show('success'),
  error: show('error'),
  info: show('info'),
  warning: show('warning'),
  loading: show('loading'),
  destroy: (key?: string) => toast.dismiss(key),
}
export function ToastHost() {
  return (
    <Toaster
      position="top-center"
      closeButton
      toastOptions={{
        className: 'ui-toast',
        style: {
          background: 'var(--console-container)',
          color: 'var(--console-ink)',
          border: '1px solid var(--console-border)',
          fontFamily: 'var(--app-font)',
        },
      }}
    />
  )
}
