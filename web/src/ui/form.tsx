import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
} from 'react'
import {
  useForm as useHookForm,
  useController,
  useWatch,
  useFieldArray,
  type UseFormReturn,
} from 'react-hook-form'
import { Label } from '@/components/ui/label'
import { Field, FieldDescription, FieldError, FieldLabel } from '@/components/ui/field'
import { cn } from '@/lib/utils'
import { message } from './feedback'
import { InputNumber } from './controls'
import { errMsg } from '../api'

type Values = Record<string, any>
type Name = string | number | (string | number)[]
export type FormInstance<T extends Values = any> = {
  getFieldValue(name: Name): any
  getFieldsValue(): T
  setFieldValue(name: Name, value: any): void
  setFieldsValue(values: Partial<T>): void
  resetFields(): void
  validateFields(): Promise<T>
  scrollToField(name: Name, options?: ScrollIntoViewOptions): void
  _raw: UseFormReturn<Values>
  _initial: React.MutableRefObject<Values>
  _id: string
  _names: Set<string>
}
const FormContext = createContext<FormInstance | null>(null)
const ListContext = createContext<string>('')
const RequiredMarkContext = createContext(true)
const FieldLayoutContext = createContext(false)
const ItemErrorsContext = createContext<((id: string, error?: string) => void) | null>(null)
const path = (name: Name) =>
  Array.isArray(name) ? name.join('.') : String(name)

function useForm<T extends Values = any>(): [FormInstance<T>] {
  const raw = useHookForm<Values>({
    mode: 'onChange',
    reValidateMode: 'onChange',
    shouldUnregister: false,
  })
  const initial = useRef<Values>({})
  const id = useId()
  const names = useRef(new Set<string>())
  const instance = useRef<FormInstance<T>>()
  if (!instance.current)
    instance.current = {
      _raw: raw,
      _initial: initial,
      _id: id,
      _names: names.current,
      getFieldValue: (name) => raw.getValues(path(name)),
      getFieldsValue: () => raw.getValues() as T,
      setFieldValue: (name, value) => raw.setValue(path(name), value, {
        shouldValidate: raw.getFieldState(path(name)).invalid,
      }),
      setFieldsValue: (values) =>
        Object.entries(values).forEach(([key, value]) =>
          raw.setValue(key, value, { shouldValidate: raw.getFieldState(key).invalid }),
        ),
      resetFields: () => raw.reset(initial.current),
      validateFields: async () => {
        if (!(await raw.trigger([...names.current]))) {
          const errorFields = [...names.current].flatMap((name) => {
            const error = raw.getFieldState(name).error
            return error
              ? [
                  {
                    name: name.split('.'),
                    errors: [error.message || '输入不正确'],
                  },
                ]
              : []
          })
          throw { errorFields, values: raw.getValues() }
        }
        return raw.getValues() as T
      },
      scrollToField: (name, options) => {
        const field = document.getElementById(`${id}-${path(name)}`)
        for (let parent = field?.parentElement; parent; parent = parent.parentElement)
          if (parent.tagName === 'DETAILS') parent.setAttribute('open', '')
        field?.scrollIntoView(options)
        field?.focus({ preventScroll: true })
      },
    }
  // Subscribe to errors for validateFields' recursive extraction.
  void raw.formState.errors
  return [instance.current]
}

type RuleObject = {
  required?: boolean
  whitespace?: boolean
  message?: string
  min?: number
  max?: number
  len?: number
  pattern?: RegExp
  type?: string
  validator?: (rule: RuleObject, value: any) => Promise<void> | void
}
type Rule = RuleObject | ((form: FormInstance) => RuleObject)
type ItemProps = {
  name?: Name
  label?: React.ReactNode
  children?: React.ReactNode
  rules?: Rule[]
  className?: string
  style?: React.CSSProperties
  noStyle?: boolean
  required?: boolean
  extra?: React.ReactNode
  help?: React.ReactNode
  valuePropName?: string
  getValueFromEvent?: (...args: any[]) => any
  getValueProps?: (value: any) => Values
  dependencies?: Name[]
  initialValue?: any
  hidden?: boolean
  fieldKey?: number
}
function FormItem(props: ItemProps) {
  if (props.name !== undefined) return <BoundItem {...props} />
  return <ItemLayout {...props}>{props.children}</ItemLayout>
}
function ItemLayout({
  label,
  children,
  className,
  style,
  noStyle,
  required,
  extra,
  help,
  hidden,
  id,
  error,
}: ItemProps & { id?: string; error?: string }) {
  const showRequiredMark = useContext(RequiredMarkContext)
  const fieldLayout = useContext(FieldLayoutContext)
  const reportToParent = useContext(ItemErrorsContext)
  const [childErrors, setChildErrors] = useState<Record<string, string>>({})
  const reportError = useCallback((fieldId: string, message?: string) => {
    setChildErrors((current) => {
      if (current[fieldId] === message) return current
      const next = { ...current }
      if (message) next[fieldId] = message
      else delete next[fieldId]
      return next
    })
  }, [])
  useEffect(() => {
    if (!noStyle || !reportToParent || !id) return
    reportToParent(id, error)
    return () => reportToParent(id)
  }, [noStyle, reportToParent, id, error])
  if (noStyle)
    return (
      <>
        {children}
        {error && !reportToParent && (
          <span id={id ? `${id}-error` : undefined} className="ui-form-item-explain" role="alert">
            {error}
          </span>
        )}
      </>
    )
  if (fieldLayout) return (
    <Field className={className} style={style} hidden={hidden} data-invalid={!!error || Object.keys(childErrors).length > 0 || undefined}>
      {label && <FieldLabel htmlFor={id}>{label}{required && showRequiredMark && <span className="ui-required">*</span>}</FieldLabel>}
      <ItemErrorsContext.Provider value={reportError}>{children}</ItemErrorsContext.Provider>
      {Object.entries(childErrors).map(([fieldId, message]) => <FieldError key={fieldId} id={`${fieldId}-error`}>{message}</FieldError>)}
      {(error || help) && <FieldError id={id ? `${id}-error` : undefined}>{error || help}</FieldError>}
      {extra && <FieldDescription>{extra}</FieldDescription>}
    </Field>
  )
  return (
    <div
      className={cn(
        'ui-form-item',
        (error || Object.keys(childErrors).length > 0) && 'ui-form-item-has-error',
        className,
      )}
      style={style}
      hidden={hidden}
    >
      <div className="ui-form-item-row">
        {label && (
          <div className="ui-form-item-label">
            <Label htmlFor={id}>
              {label}
              {required && showRequiredMark && <span className="ui-required">*</span>}
            </Label>
          </div>
        )}
        <div className="ui-form-item-control">
          <ItemErrorsContext.Provider value={reportError}>
            {children}
          </ItemErrorsContext.Provider>
          {Object.entries(childErrors).map(([fieldId, message]) => (
            <div key={fieldId} id={`${fieldId}-error`} className="ui-form-item-explain" role="alert">
              {message}
            </div>
          ))}
          {(error || help) && (
            <div
              id={id ? `${id}-error` : undefined}
              className="ui-form-item-explain"
              role={error ? 'alert' : undefined}
            >
              {error || help}
            </div>
          )}
          {extra && <div className="ui-form-item-extra">{extra}</div>}
        </div>
      </div>
    </div>
  )
}
function BoundItem(props: ItemProps) {
  const form = useContext(FormContext)!
  const prefix = useContext(ListContext)
  const name = [prefix, path(props.name!)].filter(Boolean).join('.')
  const child = React.Children.only(props.children) as React.ReactElement<any>
  const rules = (props.rules || []).map((rule) =>
    typeof rule === 'function' ? rule(form) : rule,
  )
  const { field, fieldState } = useController({
    name,
    control: form._raw.control,
    defaultValue: props.initialValue,
    rules: {
      validate: async (value) => {
        if (child.type === InputNumber && value !== undefined && value !== null && value !== '') {
          const number = Number(value)
          const { min, max, precision, step } = child.props
          if (!Number.isFinite(number)) return '请输入有效数值'
          if (min !== undefined && number < Number(min)) return `数值不能小于 ${min}`
          if (max !== undefined && number > Number(max)) return `数值不能大于 ${max}`
          if (precision === 0 && !Number.isSafeInteger(number)) return '请输入有效整数'
          if (precision > 0) {
            const scaled = number * 10 ** precision
            if (Math.abs(scaled - Math.round(scaled)) > Number.EPSILON * Math.max(1, Math.abs(scaled)) * 8)
              return `最多保留 ${precision} 位小数`
          }
          if (step !== undefined && step !== 'any' && Number(step) > 0) {
            const steps = (number - Number(min ?? 0)) / Number(step)
            if (Math.abs(steps - Math.round(steps)) > Number.EPSILON * Math.max(1, Math.abs(steps)) * 8)
              return `请按 ${step} 的步长输入`
          }
        }
        for (const rule of rules) {
          const empty =
            value === undefined ||
            value === null ||
            value === '' ||
            (Array.isArray(value) && !value.length) ||
            (rule.whitespace && typeof value === 'string' && !value.trim())
          if (rule.required && empty)
            return (
              rule.message ||
              `请填写${typeof props.label === 'string' ? props.label : '此项'}`
            )
          if (!empty) {
            const size = typeof value === 'number' ? value : value.length
            if (
              (rule.min !== undefined && size < rule.min) ||
              (rule.max !== undefined && size > rule.max) ||
              (rule.len !== undefined && size !== rule.len)
            )
              return rule.message || '输入长度或数值不符合要求'
            if (rule.pattern && !rule.pattern.test(value))
              return rule.message || '输入格式不正确'
            if (
              rule.type === 'email' &&
              !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value)
            )
              return rule.message || '邮箱格式不正确'
          }
          try {
            await rule.validator?.(rule, value)
          } catch (error) {
            return error instanceof Error
              ? error.message
              : rule.message || '输入格式不正确'
          }
        }
        return true
      },
    },
  })
  const dependencyValues = useWatch({
    control: form._raw.control,
    name: (props.dependencies || []).map(path),
    disabled: !props.dependencies?.length,
  })
  useEffect(() => {
    if (!props.dependencies?.length) return
    const state = form._raw.getFieldState(name)
    if (state.isDirty || state.isTouched || state.invalid)
      void form._raw.trigger(name)
  }, [dependencyValues, form, name])
  useEffect(() => {
    form._names.add(name)
    return () => {
      form._names.delete(name)
    }
  }, [form, name])
  const id = `${form._id}-${name}`
  const valueProps = props.getValueProps
    ? props.getValueProps(field.value)
    : {
        [props.valuePropName || 'value']:
          field.value ?? (props.valuePropName === 'checked' ? false : ''),
      }
  const control = React.cloneElement(child, {
    ...valueProps,
    id,
    ref: field.ref,
    'aria-invalid': fieldState.invalid || undefined,
    'aria-describedby': fieldState.error ? `${id}-error` : undefined,
    onBlur: (...args: any[]) => {
      field.onBlur()
      child.props.onBlur?.(...args)
    },
    onChange: (...args: any[]) => {
      const first = args[0]
      const value = props.getValueFromEvent
        ? props.getValueFromEvent(...args)
        : first?.target
          ? first.target[
              props.valuePropName === 'checked' ? 'checked' : 'value'
            ]
          : first
      field.onChange(value)
      child.props.onChange?.(...args)
    },
  })
  return (
    <ItemLayout
      {...props}
      id={id}
      error={fieldState.error?.message}
      required={props.required || rules.some((rule) => rule.required)}
    >
      {control}
    </ItemLayout>
  )
}
function FormRoot({
  form: provided,
  children,
  initialValues,
  onFinish,
  onFinishFailed,
  className,
  layout = 'vertical',
  requiredMark,
  fieldLayout = false,
  name,
  ...rest
}: Omit<React.FormHTMLAttributes<HTMLFormElement>, 'onFinish'> & {
  form?: FormInstance
  initialValues?: Values
  onFinish?: (values: any) => void | Promise<void>
  onFinishFailed?: (error: any) => void
  layout?: string
  requiredMark?: boolean
  fieldLayout?: boolean
  name?: string
}) {
  const [fallback] = useForm()
  const form = provided || fallback
  const initialized = useRef(false)
  const submitting = useRef(false)
  useEffect(() => {
    if (initialized.current) return
    initialized.current = true
    form._initial.current = initialValues || {}
    for (const [key, value] of Object.entries(initialValues || {}))
      if (form.getFieldValue(key) === undefined) form.setFieldValue(key, value)
  }, [form, initialValues])
  return (
    <FormContext.Provider value={form}>
      <form
        {...rest}
        name={name}
        className={cn('ui-form', `ui-form-${layout}`, className)}
        onSubmit={async (event) => {
          event.preventDefault()
          if (submitting.current) return
          submitting.current = true
          try {
            const values = await form.validateFields()
            await onFinish?.(values)
          } catch (error: any) {
            if (error?.errorFields) {
              onFinishFailed?.(error)
              if (error.errorFields[0])
                form.scrollToField(error.errorFields[0].name, { block: 'center' })
            } else {
              message.error(errMsg(error))
            }
          } finally {
            submitting.current = false
          }
        }}
      >
        <RequiredMarkContext.Provider value={requiredMark !== false}>
          <FieldLayoutContext.Provider value={fieldLayout}>{children}</FieldLayoutContext.Provider>
        </RequiredMarkContext.Provider>
      </form>
    </FormContext.Provider>
  )
}
function FormList({
  name,
  children,
}: {
  name: string
  children: (
    fields: { key: string; name: number }[],
    operations: {
      add: (value: any) => void
      remove: (index: number) => void
      move: (from: number, to: number) => void
    },
  ) => React.ReactNode
}) {
  const form = useContext(FormContext)!
  const list = useFieldArray({ name, control: form._raw.control })
  return (
    <ListContext.Provider value={name}>
      {children(
        list.fields.map((field, index) => ({ key: field.id, name: index })),
        {
          add: (value) => list.append(value),
          remove: list.remove,
          move: list.move,
        },
      )}
    </ListContext.Provider>
  )
}
function watch(name: Name, form: FormInstance) {
  return useWatch({ name: path(name), control: form._raw.control })
}
export const Form = Object.assign(FormRoot, {
  Item: FormItem,
  List: FormList,
  useForm,
  useWatch: watch,
})
