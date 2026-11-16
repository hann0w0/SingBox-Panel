import { useState } from 'react'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { Pills } from './Page'

afterEach(cleanup)

it('skips disabled pills when moving, wrapping, and jumping to either end', () => {
  const onChange = vi.fn()
  function Tabs() {
    const [active, setActive] = useState('a')
    return <Pills active={active} onChange={(key) => { setActive(key); onChange(key) }} items={[
      { key: 'start', label: 'Disabled start', disabled: true },
      { key: 'a', label: 'First' },
      { key: 'middle', label: 'Disabled middle', disabled: true },
      { key: 'b', label: 'Last' },
      { key: 'end', label: 'Disabled end', disabled: true },
    ]} />
  }
  render(<Tabs />)
  const first = screen.getByRole('tab', { name: 'First' })
  const last = screen.getByRole('tab', { name: 'Last' })
  first.focus()
  for (const [key, destination] of [['ArrowRight', last], ['ArrowRight', first], ['ArrowLeft', last], ['Home', first], ['End', last]] as const) {
    fireEvent.keyDown(document.activeElement!, { key })
    expect(document.activeElement).toBe(destination)
    expect(destination.getAttribute('aria-selected')).toBe('true')
  }
  expect(onChange.mock.calls.map(([key]) => key)).toEqual(['b', 'a', 'b', 'a', 'b'])
})

it('makes the first enabled pill tabbable when the active pill is disabled', () => {
  render(<Pills active="a" onChange={vi.fn()} items={[{ key: 'a', label: 'Disabled', disabled: true }, { key: 'b', label: 'Enabled' }]} />)
  expect(screen.getByRole('tab', { name: 'Enabled' }).tabIndex).toBe(0)
  expect(screen.getByRole('tab', { name: 'Disabled' }).tabIndex).toBe(-1)
})

it('does not select any pill when every pill is disabled', () => {
  const onChange = vi.fn()
  render(<Pills active="a" onChange={onChange} items={[{ key: 'a', label: 'A', disabled: true }, { key: 'b', label: 'B', disabled: true }]} />)
  for (const tab of screen.getAllByRole('tab')) {
    expect(tab.tabIndex).toBe(-1)
    for (const key of ['ArrowRight', 'ArrowLeft', 'Home', 'End']) fireEvent.keyDown(tab, { key })
    fireEvent.click(tab)
  }
  expect(onChange).not.toHaveBeenCalled()
})
