import { createRef } from 'react'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogOverlay, DialogPortal, DialogTitle, DialogTrigger } from './dialog'

afterEach(cleanup)

it('forwards primitive refs in React 18 without warnings and returns focus to its trigger', async () => {
  const error = vi.spyOn(console, 'error')
  const trigger = createRef<HTMLButtonElement>()
  const close = createRef<HTMLButtonElement>()
  const overlay = createRef<HTMLDivElement>()
  const content = createRef<HTMLDivElement>()
  const title = createRef<HTMLHeadingElement>()
  const description = createRef<HTMLParagraphElement>()
  render(<Dialog><DialogTrigger ref={trigger}>Open</DialogTrigger><DialogPortal><DialogOverlay ref={overlay} /></DialogPortal><DialogContent ref={content} showCloseButton={false}><DialogTitle ref={title}>Test dialog</DialogTitle><DialogDescription ref={description}>Details</DialogDescription><DialogClose ref={close}>Done</DialogClose></DialogContent></Dialog>)
  trigger.current!.focus()
  fireEvent.click(trigger.current!)
  expect(overlay.current?.dataset.slot).toBe('dialog-overlay')
  expect(content.current).toBe(screen.getByRole('dialog'))
  expect(title.current?.textContent).toBe('Test dialog')
  expect(description.current?.textContent).toBe('Details')
  expect(close.current).toBe(screen.getByRole('button', { name: 'Done' }))
  fireEvent.click(close.current!)
  expect(screen.queryByRole('dialog')).toBeNull()
  await waitFor(() => expect(document.activeElement).toBe(trigger.current))
  expect(error.mock.calls.flat().join(' ')).not.toContain('cannot be given refs')
})
