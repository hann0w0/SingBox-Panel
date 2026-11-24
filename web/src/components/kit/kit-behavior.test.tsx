import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { Button, InputNumber, Select } from './controls'
import { Upload } from './upload'

describe('shared kit behavior', () => {
  it('shows the selected option in uncontrolled Select mode', () => {
    render(<Select defaultValue="firefox" onChange={() => {}} options={[{ value: 'chrome', label: 'Chrome' }, { value: 'firefox', label: 'Firefox' }]} />)
    expect(screen.getByText('Firefox')).toBeTruthy()
  })

  it('does not add files rejected by beforeUpload', () => {
    const onChange = vi.fn()
    render(
      <Upload.Dragger beforeUpload={() => false} onChange={onChange}>
        <span>upload</span>
      </Upload.Dragger>,
    )
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    fireEvent.change(input, { target: { files: [new File(['x'], 'blocked.txt')] } })
    expect(onChange).toHaveBeenCalledWith({ fileList: [] })
  })

  it('keeps the link button accessible and themed', () => {
    render(<Button href="/admin/servers" aria-label="servers" type="primary">Servers</Button>)
    const link = screen.getByRole('link', { name: 'servers' })
    expect(link.className).toContain('ui-btn-primary')
  })

  it('blurs a focused number input on wheel to avoid accidental edits', () => {
    render(<InputNumber aria-label="port" value={443} />)
    const input = screen.getByRole('spinbutton', { name: 'port' }) as HTMLInputElement
    input.focus()
    fireEvent.wheel(input)
    expect(document.activeElement).not.toBe(input)
  })
})
