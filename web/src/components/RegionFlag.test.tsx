import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { RegionFlag, regionCodeFromFlag, removeRegionFlag } from './RegionFlag'

describe('RegionFlag', () => {
  it('renders a Unicode flag without the flag-icons stylesheet classes', () => {
    render(<RegionFlag code="HK" size={20} />)
    const flag = screen.getByRole('img', { name: 'HK' })
    expect(flag.textContent).toBe('🇭🇰')
    expect(flag.className).not.toContain('fi')
    expect(flag.getAttribute('style')).toContain('width: 30px')
  })

  it('keeps flag parsing helpers compatible with imported node names', () => {
    expect(regionCodeFromFlag('🇯🇵 Tokyo')).toBe('JP')
    expect(removeRegionFlag('🇯🇵  Tokyo')).toBe('Tokyo')
  })
})
