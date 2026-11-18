import { useId } from 'react'
import { Input, InputNumber, Select, Switch } from './kit'
import { Field, FieldLabel } from '@/components/ui/field'
import type { JSONSchema } from '../lib/singboxSchema'
import { JsonCodeEditor } from './JsonCodeEditor'

function sortSchemaEntries(entries: [string, JSONSchema][]) {
  return [...entries].sort(([, a], [, b]) => {
    const ao = a['x-ui']?.order ?? 999
    const bo = b['x-ui']?.order ?? 999
    return ao - bo
  })
}

function splitLines(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean)
}

function joinLines(arr: unknown): string {
  return Array.isArray(arr) ? arr.map(String).join('\n') : ''
}

export function SchemaFieldList({
  properties,
  value,
  onChange,
  excludeKeys,
  idPrefix = 'schema',
}: {
  properties: Record<string, JSONSchema>
  value: Record<string, unknown>
  onChange: (next: Record<string, unknown>) => void
  excludeKeys?: Set<string>
  idPrefix?: string
}) {
  const autoId = useId()
  const entries = sortSchemaEntries(
    Object.entries(properties).filter(([key]) => !excludeKeys?.has(key)),
  )

  if (entries.length === 0) return null

  const updateKey = (key: string, nextVal: unknown) => {
    const next = { ...value }
    if (
      nextVal === undefined ||
      nextVal === null ||
      nextVal === '' ||
      (Array.isArray(nextVal) && nextVal.length === 0)
    ) {
      delete next[key]
    } else {
      next[key] = nextVal
    }
    onChange(next)
  }

  const switchEntries = entries.filter(
    ([, schema]) => schema.type === 'boolean' || schema['x-ui']?.widget === 'switch',
  )
  const objectEntries = entries.filter(
    ([, schema]) => schema.type === 'object' && schema.properties && schema['x-ui']?.widget !== 'json',
  )
  const regularEntries = entries.filter(
    ([, schema]) =>
      schema.type !== 'boolean' &&
      schema['x-ui']?.widget !== 'switch' &&
      !(schema.type === 'object' && schema.properties && schema['x-ui']?.widget !== 'json'),
  )

  return (
    <>
      {regularEntries.map(([key, schema]) => {
        const ui = schema['x-ui']
        const label = ui?.label || key
        const fieldId = `${idPrefix}-${autoId}-${key}`
        const current = value[key]

        if (schema.type === 'object' || ui?.widget === 'json') {
          const textVal =
            current && typeof current === 'object'
              ? JSON.stringify(current, null, 2)
              : ''
          return (
            <Field key={key} className="fm-full">
              <FieldLabel htmlFor={fieldId}>{label}</FieldLabel>
              <JsonCodeEditor
                ariaLabel={label}
                value={textVal}
                onChange={(raw) => {
                  const trimmed = raw.trim()
                  if (!trimmed) {
                    updateKey(key, undefined)
                    return
                  }
                  try {
                    const parsed = JSON.parse(trimmed)
                    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
                      updateKey(key, parsed)
                    }
                  } catch {
                    // Keep editing until JSON becomes valid
                  }
                }}
              />
            </Field>
          )
        }

        if (schema.type === 'array') {
          const isIntArray = schema.items?.type === 'integer' || schema.items?.type === 'number'
          if (ui?.widget === 'tags' || ui?.widget === 'multiselect') {
            return (
              <Field key={key} className={ui?.full_width ? 'fm-full' : undefined}>
                <FieldLabel htmlFor={fieldId}>{label}</FieldLabel>
                <Select
                  id={fieldId}
                  mode="tags"
                  allowClear
                  placeholder={ui?.placeholder}
                  value={Array.isArray(current) ? current.map(String) : []}
                  onChange={(vals: string[]) => {
                    if (isIntArray) {
                      const nums = vals.map(Number).filter((n) => Number.isInteger(n))
                      updateKey(key, nums)
                    } else {
                      updateKey(key, vals)
                    }
                  }}
                  options={[]}
                />
              </Field>
            )
          }

          return (
            <Field key={key} className={ui?.full_width ? 'fm-full' : undefined}>
              <FieldLabel htmlFor={fieldId}>{label}</FieldLabel>
              <Input.TextArea
                id={fieldId}
                autoSize={{ minRows: 1, maxRows: 4 }}
                className="mono"
                placeholder={ui?.placeholder}
                value={joinLines(current)}
                onChange={(e) => {
                  const items = splitLines(e.target.value)
                  if (isIntArray) {
                    const nums = items.map(Number).filter((n) => !Number.isNaN(n) && Number.isInteger(n))
                    updateKey(key, nums)
                  } else {
                    updateKey(key, items)
                  }
                }}
              />
            </Field>
          )
        }

        if (schema.enum && schema.enum.length > 0) {
          const isNumEnum = schema.type === 'integer' || schema.type === 'number'
          return (
            <Field key={key} className={ui?.full_width ? 'fm-full' : undefined}>
              <FieldLabel htmlFor={fieldId}>{label}</FieldLabel>
              <Select
                id={fieldId}
                allowClear
                placeholder={ui?.placeholder}
                value={current !== undefined && current !== '' ? (current as string | number) : undefined}
                onChange={(val) => updateKey(key, val === undefined || val === '' ? undefined : val)}
                options={schema.enum
                  .filter((opt) => opt !== '')
                  .map((opt) => ({
                    value: isNumEnum ? Number(opt) : String(opt),
                    label: String(opt),
                  }))}
              />
            </Field>
          )
        }

        if (schema.type === 'integer' || schema.type === 'number') {
          return (
            <Field key={key} className={ui?.full_width ? 'fm-full' : undefined}>
              <FieldLabel htmlFor={fieldId}>{label}</FieldLabel>
              <InputNumber
                id={fieldId}
                className="mono"
                min={schema.minimum}
                max={schema.maximum}
                precision={schema.type === 'integer' ? 0 : undefined}
                value={typeof current === 'number' ? current : undefined}
                onChange={(val) => updateKey(key, val === null ? undefined : val)}
              />
            </Field>
          )
        }

        if (ui?.widget === 'password') {
          return (
            <Field key={key} className={ui?.full_width ? 'fm-full' : undefined}>
              <FieldLabel htmlFor={fieldId}>{label}</FieldLabel>
              <Input.Password
                id={fieldId}
                className="mono"
                placeholder={ui?.placeholder}
                value={typeof current === 'string' ? current : ''}
                onChange={(e) => updateKey(key, e.target.value)}
              />
            </Field>
          )
        }

        return (
          <Field key={key} className={ui?.full_width ? 'fm-full' : undefined}>
            <FieldLabel htmlFor={fieldId}>{label}</FieldLabel>
            <Input
              id={fieldId}
              className="mono"
              placeholder={ui?.placeholder}
              value={typeof current === 'string' ? current : ''}
              onChange={(e) => updateKey(key, e.target.value)}
            />
          </Field>
        )
      })}

      {switchEntries.length > 0 && (
        <div className="fm-switches">
          {switchEntries.map(([key, schema]) => {
            const ui = schema['x-ui']
            const label = ui?.label || key
            const fieldId = `${idPrefix}-${autoId}-${key}`
            const checked = value[key] !== undefined ? Boolean(value[key]) : Boolean(schema.default)
            return (
              <div key={key} className="ui-form-item fm-switch">
                <div className="ui-form-item-row">
                  <div className="ui-form-item-label">
                    <label htmlFor={fieldId}>{label}</label>
                  </div>
                  <div className="ui-form-item-control">
                    <Switch
                      id={fieldId}
                      checked={checked}
                      onCheckedChange={(nextChecked) => {
                        if (nextChecked) {
                          updateKey(key, true)
                        } else if (schema.default === true) {
                          updateKey(key, false)
                        } else {
                          updateKey(key, undefined)
                        }
                      }}
                    />
                  </div>
                </div>
              </div>
            )
          })}
        </div>
      )}

      {objectEntries.map(([key, schema]) => {
        const ui = schema['x-ui']
        const label = ui?.label || key
        const current = value[key]
        const objVal =
          current && typeof current === 'object' && !Array.isArray(current)
            ? (current as Record<string, unknown>)
            : {}
        return (
          <div key={key} className="fm-full border-t border-[var(--console-border)] pt-3 mt-1">
            <div className="text-[12px] font-medium mb-2 text-[var(--console-muted)]">{label}</div>
            <div className="fm-grid">
              <SchemaFieldList
                properties={schema.properties!}
                value={objVal}
                onChange={(nested) => {
                  if (Object.keys(nested).length === 0) {
                    updateKey(key, undefined)
                  } else {
                    updateKey(key, nested)
                  }
                }}
                idPrefix={`${idPrefix}-${key}`}
              />
            </div>
          </div>
        )
      })}
    </>
  )
}
