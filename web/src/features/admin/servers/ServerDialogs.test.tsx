import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { ConfigEditorModal } from './ServerDialogs'

afterEach(cleanup)

it('shows offline configuration as a read-only preview without applying it', () => {
  render(<ConfigEditorModal open loading={false} saving={false} preview text={'{"inbounds":[]}'} onText={vi.fn()} onCancel={vi.fn()} onSave={vi.fn()} />)
  expect(screen.getByRole('heading', { name: '配置预览（未下发）' })).toBeTruthy()
  expect((screen.getByRole('textbox', { name: 'config.json' }) as HTMLTextAreaElement).readOnly).toBe(true)
  expect(screen.queryByRole('button', { name: '校验并下发' })).toBeNull()
})

it('keeps the host configuration editable when online', () => {
  render(<ConfigEditorModal open loading={false} saving={false} text={'{"inbounds":[]}'} onText={vi.fn()} onCancel={vi.fn()} onSave={vi.fn()} />)
  expect((screen.getByRole('textbox', { name: 'config.json' }) as HTMLTextAreaElement).readOnly).toBe(false)
  expect(screen.getByRole('button', { name: '校验并下发' })).toBeTruthy()
})
