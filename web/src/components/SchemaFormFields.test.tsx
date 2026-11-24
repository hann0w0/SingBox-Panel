import { fireEvent, render, screen } from '@testing-library/react'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { SchemaFieldList } from './SchemaFormFields'

describe('SchemaFieldList', () => {
  beforeAll(() => {
    vi.stubGlobal('ResizeObserver', class {
      observe() {}
      unobserve() {}
      disconnect() {}
    })
    Element.prototype.scrollIntoView = vi.fn()
  })

  it('offers enum options for tag fields instead of an empty option list', () => {
    render(
      <SchemaFieldList
        properties={{ protocol: { type: 'array', items: { type: 'string', enum: ['h2', 'http/1.1'] }, 'x-ui': { label: '协议', widget: 'tags' } } }}
        value={{}}
        onChange={() => {}}
      />,
    )
    fireEvent.click(screen.getByRole('combobox', { name: '协议' }))
    expect(screen.getByText('h2')).toBeTruthy()
    expect(screen.getByText('http/1.1')).toBeTruthy()
  })
})
