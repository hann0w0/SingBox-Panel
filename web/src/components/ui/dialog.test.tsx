import { createRef } from 'react'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogOverlay, DialogPortal, DialogTitle, DialogTrigger } from './dialog'
import { Popover, PopoverContent, PopoverTrigger } from './popover'
import { Table } from '../kit/table'

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

it('dismisses a nested popover when clicking blank space inside a dialog', async () => {
  render(
    <Dialog open>
      <DialogContent showCloseButton={false}>
        <DialogTitle>Dialog Title</DialogTitle>
        <DialogDescription>Blank area</DialogDescription>
        <Popover>
          <PopoverTrigger asChild>
            <button type="button">Open options</button>
          </PopoverTrigger>
          <PopoverContent>Option body</PopoverContent>
        </Popover>
      </DialogContent>
    </Dialog>,
  )
  fireEvent.click(screen.getByRole('button', { name: 'Open options' }))
  expect(await screen.findByText('Option body')).toBeTruthy()

  // Wait a macrotask so Radix DismissableLayer attaches its document pointerdown listener.
  await new Promise((resolve) => setTimeout(resolve, 0))
  const blank = screen.getByText('Blank area')
  fireEvent.pointerDown(blank, { button: 0 })
  fireEvent.click(blank)

  await waitFor(() => expect(screen.queryByText('Option body')).toBeNull())
  expect(screen.getByRole('dialog')).toBeTruthy()
})

it('ignores clicks inside portaled popovers on clickable table rows', async () => {
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  const onRowClick = vi.fn()
  try {
    render(
      <Table
        rowKey="id"
        dataSource={[{ id: 1, name: 'Row 1' }]}
        onRow={() => ({ onClick: onRowClick })}
        columns={[
          {
            title: 'Name',
            render: () => (
              <Popover>
                <PopoverTrigger asChild>
                  <button type="button">Edit group</button>
                </PopoverTrigger>
                <PopoverContent>
                  <div>Inside portal</div>
                </PopoverContent>
              </Popover>
            ),
          },
        ]}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Edit group' }))
    onRowClick.mockClear()
    fireEvent.click(await screen.findByText('Inside portal'))
    expect(onRowClick).not.toHaveBeenCalled()
  } finally {
    vi.unstubAllGlobals()
  }
})
