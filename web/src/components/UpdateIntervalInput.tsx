import { forwardRef } from 'react'
import { InputNumber, Select } from './kit'
import {
  UPDATE_INTERVAL_UNIT_MINUTES,
  UPDATE_INTERVAL_UNIT_OPTIONS,
  type UpdateIntervalUnit,
  type UpdateIntervalValue,
} from '../lib/updateInterval'

export interface UpdateIntervalInputProps {
  id?: string
  value?: UpdateIntervalValue
  onChange?: (next: UpdateIntervalValue) => void
  onBlur?: () => void
  disabled?: boolean
  maxMinutes?: number
}

export const UpdateIntervalInput = forwardRef<HTMLInputElement, UpdateIntervalInputProps>(
  function UpdateIntervalInput(
    { id, value, onChange, onBlur, disabled, maxMinutes = 43200 },
    ref,
  ) {
    const current: UpdateIntervalValue = value ?? { amount: 1, unit: 'h' }
    const maxForUnit = Math.max(1, Math.floor(maxMinutes / UPDATE_INTERVAL_UNIT_MINUTES[current.unit]))

    return (
      <div className="ui-interval-input">
        <InputNumber
          ref={ref}
          id={id}
          min={1}
          max={maxForUnit}
          precision={0}
          disabled={disabled}
          className="mono"
          value={current.amount}
          onBlur={onBlur}
          onChange={(amount) => onChange?.({ ...current, amount })}
        />
        <Select
          aria-label="更新间隔单位"
          disabled={disabled}
          value={current.unit}
          options={UPDATE_INTERVAL_UNIT_OPTIONS}
          onChange={(unit) => onChange?.({ ...current, unit: unit as UpdateIntervalUnit })}
        />
      </div>
    )
  },
)
