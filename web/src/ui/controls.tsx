import React, { forwardRef, useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import {
  Check,
  ChevronDown,
  Copy,
  Eye,
  EyeOff,
  LoaderCircle,
  X,
  CircleAlert,
} from 'lucide-react'
import {
  ToggleGroup,
  Popover as PopoverPrimitive,
} from 'radix-ui'
import { buttonVariants } from '@/components/ui/button'
import { Input as InputPrimitive } from '@/components/ui/input'
import { Textarea as TextareaPrimitive } from '@/components/ui/textarea'
import { Checkbox as CheckboxPrimitive } from '@/components/ui/checkbox'
import { Switch as SwitchPrimitive } from '@/components/ui/switch'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import {
  Command,
  CommandInput,
  CommandList,
  CommandItem,
  CommandEmpty,
} from '@/components/ui/command'
import {
  Tooltip as TooltipRoot,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'
import { copyToClipboard } from '../util'
import { message } from './feedback'

export type ButtonProps = Omit<
  React.ButtonHTMLAttributes<HTMLButtonElement>,
  'type'
> & {
  type?: 'primary' | 'default' | 'text' | 'link' | 'dashed' | 'danger'
  htmlType?: 'button' | 'submit' | 'reset'
  danger?: boolean
  loading?: boolean
  size?: 'small' | 'middle' | 'large'
  block?: boolean
  icon?: React.ReactNode
  href?: string
  target?: string
  rel?: string
}
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  function Button(
    {
      type = 'default',
      htmlType = 'button',
      danger,
      loading,
      size,
      block,
      icon,
      children,
      className,
      disabled,
      href,
      target,
      rel,
      ...props
    },
    ref,
  ) {
    // Radix Slot (e.g. DropdownMenuTrigger / PopoverTrigger asChild) forwards
    // a native HTML `type="button"` prop when `Button` has no explicit `type`,
    // which would otherwise replace the default visual variant (`ui-btn-default`).
    const resolvedType =
      type === 'primary' ||
      type === 'text' ||
      type === 'link' ||
      type === 'dashed' ||
      type === 'danger'
        ? type
        : 'default'
    const variant =
      resolvedType === 'primary'
        ? danger
          ? 'destructive'
          : 'default'
        : resolvedType === 'text'
          ? 'ghost'
          : resolvedType === 'link'
            ? 'link'
            : 'outline'
    if (href)
      return (
        <a
          href={disabled ? undefined : href}
          target={target}
          rel={rel}
          className={cn(
            buttonVariants({ variant, size: 'default' }),
            'ui-btn',
            className,
          )}
        >
          {icon}
          {children}
        </a>
      )
    return (
      <button
        {...props}
        ref={ref}
        type={htmlType}
        disabled={disabled || loading}
        aria-busy={loading || undefined}
        className={cn(
          buttonVariants({
            variant,
            size: size === 'small' ? 'sm' : size === 'large' ? 'lg' : 'default',
          }),
          'ui-btn',
          `ui-btn-${resolvedType}`,
          (danger || resolvedType === 'danger') && 'ui-btn-dangerous',
          size === 'small' && 'ui-btn-sm',
          size === 'large' && 'ui-btn-lg',
          block && 'w-full',
          resolvedType === 'dashed' && 'border-dashed',
          !children && 'ui-btn-icon-only',
          className,
        )}
      >
        {loading ? (
          <LoaderCircle className="size-4 animate-spin" />
        ) : (
          icon && <span className="ui-btn-icon">{icon}</span>
        )}
        {children}
      </button>
    )
  },
)
type InputProps = Omit<
  React.InputHTMLAttributes<HTMLInputElement>,
  'size' | 'prefix'
> & {
  size?: string
  prefix?: React.ReactNode
  suffix?: React.ReactNode
  allowClear?: boolean
  onPressEnter?: (e: React.KeyboardEvent<HTMLInputElement>) => void
}
const BaseInput = forwardRef<HTMLInputElement, InputProps>(function Input(
  {
    className,
    prefix,
    suffix,
    allowClear,
    onPressEnter,
    size,
    onKeyDown,
    onChange,
    value,
    ...props
  },
  ref,
) {
  const clear = allowClear && value !== undefined && value !== ''
  const input = (
    <InputPrimitive
      {...props}
      value={value}
      onChange={onChange}
      ref={ref}
      className={cn(
        'ui-input h-auto',
        !prefix && !suffix && !allowClear && className,
        size === 'large' && 'ui-input-lg',
      )}
      onKeyDown={(e) => {
        onKeyDown?.(e)
        if (e.key === 'Enter') onPressEnter?.(e)
      }}
    />
  )
  return prefix || suffix || allowClear ? (
    <span
      className={cn(
        'ui-input-affix-wrapper',
        props.disabled && 'ui-input-affix-wrapper-disabled',
        size === 'large' && 'ui-input-affix-wrapper-lg',
        className,
      )}
      style={props.style}
    >
      {prefix && <span className="ui-input-prefix">{prefix}</span>}
      {React.cloneElement(input, { style: undefined })}
      {clear && (
        <button
          type="button"
          aria-label="清空"
          disabled={props.disabled}
          className="ui-input-clear"
          onClick={() =>
            onChange?.({
              target: { value: '' },
              currentTarget: { value: '' },
            } as React.ChangeEvent<HTMLInputElement>)
          }
        >
          <X size={14} />
        </button>
      )}
      {suffix}
    </span>
  ) : (
    input
  )
})
const Password = forwardRef<
  HTMLInputElement,
  InputProps & { iconRender?: (visible: boolean) => React.ReactNode }
>(function Password({ iconRender, ...props }, ref) {
  const [visible, setVisible] = useState(false)
  return (
    <BaseInput
      {...props}
      ref={ref}
      type={visible ? 'text' : 'password'}
      suffix={
        <button
          className="ui-input-clear"
          type="button"
          aria-label={visible ? '隐藏密码' : '显示密码'}
          disabled={props.disabled}
          onClick={() => setVisible(!visible)}
        >
          {iconRender?.(visible) ||
            (visible ? <EyeOff size={16} /> : <Eye size={16} />)}
        </button>
      }
    />
  )
})
const TextArea = forwardRef<
  HTMLTextAreaElement,
  React.TextareaHTMLAttributes<HTMLTextAreaElement> & {
    autoSize?: boolean | { minRows?: number; maxRows?: number }
  }
>(function TextArea({ autoSize, className, ...props }, ref) {
  const inputRef = useRef<HTMLTextAreaElement | null>(null)
  const minRows = Math.max(1, typeof autoSize === 'object' ? autoSize.minRows ?? 1 : props.rows ?? 1)
  const maxRows = typeof autoSize === 'object' && autoSize.maxRows !== undefined
    ? Math.max(minRows, autoSize.maxRows)
    : Infinity
  const resize = useCallback(() => {
    const input = inputRef.current
    if (!autoSize || !input || !input.clientWidth) return
    const style = window.getComputedStyle(input)
    const lineHeight = parseFloat(style.lineHeight) || parseFloat(style.fontSize) * 1.5
    const padding = parseFloat(style.paddingTop) + parseFloat(style.paddingBottom)
    const border = parseFloat(style.borderTopWidth) + parseFloat(style.borderBottomWidth)
    input.style.height = '0px'
    const contentHeight = input.scrollHeight
    const boundedHeight = Math.max(minRows * lineHeight + padding,
      Math.min(contentHeight, maxRows * lineHeight + padding))
    input.style.height = `${boundedHeight + (style.boxSizing === 'border-box' ? border : -padding)}px`
    input.style.overflowY = contentHeight > boundedHeight ? 'auto' : 'hidden'
  }, [!!autoSize, minRows, maxRows])
  useLayoutEffect(() => {
    resize()
  }, [resize, props.value, props.defaultValue])
  useLayoutEffect(() => {
    const input = inputRef.current
    if (!autoSize || !input) return
    let width = input.clientWidth
    const observer = new ResizeObserver(() => {
      if (input.clientWidth !== width) {
        width = input.clientWidth
        resize()
      }
    })
    observer.observe(input)
    return () => {
      observer.disconnect()
      input.style.height = ''
      input.style.overflowY = ''
    }
  }, [!!autoSize, resize])
  return (
    <TextareaPrimitive
      {...props}
      ref={(node) => {
        inputRef.current = node
        if (typeof ref === 'function') ref(node)
        else if (ref) ref.current = node
      }}
      onChange={(event) => {
        props.onChange?.(event)
        resize()
      }}
      rows={props.rows ?? (typeof autoSize === 'object' ? autoSize.minRows : 3)}
      className={cn('ui-input ui-textarea', className)}
    />
  )
})
export const Input = Object.assign(BaseInput, { Password, TextArea })
export const InputNumber = forwardRef<
  HTMLInputElement,
  Omit<InputProps, 'onChange' | 'value' | 'defaultValue' | 'type'> & {
    onChange?: (value: number | null) => void
    value?: number | string | null
    precision?: number
  }
>(function InputNumber(
  {
    onChange,
    value,
    precision,
    className,
    size,
    prefix,
    suffix,
    allowClear,
    onPressEnter,
    ...props
  },
  ref,
) {
  return (
    <span className={cn('ui-input-number', className)} style={props.style}>
      <InputPrimitive
        {...props}
        style={undefined}
        ref={ref}
        type="number"
        step={props.step ?? (precision === 0 ? 1 : 'any')}
        className="ui-input-number-input h-auto border-0 shadow-none focus-visible:ring-0"
        value={value ?? ''}
        onChange={(event) =>
          onChange?.(
            event.target.value === '' ? null : Number(event.target.value),
          )
        }
      />
    </span>
  )
})
export const Switch = forwardRef<
  HTMLButtonElement,
  Omit<React.ComponentProps<typeof SwitchPrimitive>, 'onChange' | 'size'> & {
    onChange?: (checked: boolean) => void
    size?: string
  }
>(function Switch({ onChange, onCheckedChange, className, size, ...props }, ref) {
  return (
    <SwitchPrimitive
      {...props}
      ref={ref}
      size={size === 'small' ? 'sm' : 'default'}
      onCheckedChange={(checked) => {
        onChange?.(checked)
        onCheckedChange?.(checked)
      }}
      className={cn('ui-switch', className)}
    />
  )
})
type CheckboxProps = Omit<
  React.ComponentProps<typeof CheckboxPrimitive>,
  'onChange'
> & {
  onChange?: (event: { target: { checked: boolean } }) => void
  indeterminate?: boolean
}
const CheckboxRoot = forwardRef<HTMLButtonElement, CheckboxProps>(
  function Checkbox(
    { onChange, onCheckedChange, children, className, indeterminate, checked, ...props },
    ref,
  ) {
    return (
      <label
        className={cn(
          'ui-checkbox-wrapper',
          checked && 'ui-checkbox-wrapper-checked',
          className,
        )}
      >
        <CheckboxPrimitive
          {...props}
          ref={ref}
          checked={indeterminate ? 'indeterminate' : checked}
          onCheckedChange={(value) => {
            onChange?.({ target: { checked: value === true } })
            onCheckedChange?.(value)
          }}
          className="ui-checkbox"
        />
        {children && <span>{children}</span>}
      </label>
    )
  },
)
const CheckboxGroup = forwardRef<
  HTMLDivElement,
  {
    options: { label: React.ReactNode; value: string }[]
    value?: string[]
    onChange?: (value: string[]) => void
    className?: string
    id?: string
  }
>(function CheckboxGroup(
  { options, value = [], onChange, className, id },
  ref,
) {
  return (
    <div id={id} ref={ref} className={cn('ui-checkbox-group', className)}>
      {options.map((option) => (
        <CheckboxRoot
          key={option.value}
          checked={value.includes(option.value)}
          onChange={(e) =>
            onChange?.(
              e.target.checked
                ? [...value, option.value]
                : value.filter((v) => v !== option.value),
            )
          }
        >
          {option.label}
        </CheckboxRoot>
      ))}
    </div>
  )
})
export const Checkbox = Object.assign(CheckboxRoot, { Group: CheckboxGroup })
type SegmentedProps<T extends string | number> = {
  options: (T | { label: React.ReactNode; value: T; disabled?: boolean })[]
  value?: T
  onChange?: (value: T) => void
  block?: boolean
  className?: string
  'aria-label'?: string
  id?: string
}
export const Segmented = forwardRef(function Segmented<
  T extends string | number = string,
>(
  { options, value, onChange, block, className, ...props }: SegmentedProps<T>,
  ref: React.ForwardedRef<HTMLDivElement>,
) {
  const items = options.map((o) =>
    typeof o === 'object' ? o : { label: o, value: o },
  )
  return (
    <ToggleGroup.Root
      {...props}
      ref={ref}
      type="single"
      value={value === undefined ? '' : String(value)}
      onValueChange={(v) => {
        const item = items.find((o) => String(o.value) === v)
        if (item) onChange?.(item.value)
      }}
      className={cn('ui-segmented', block && 'ui-segmented-block', className)}
    >
      {items.map((option) => (
        <ToggleGroup.Item
          key={String(option.value)}
          value={String(option.value)}
          disabled={option.disabled}
          className={cn(
            'ui-segmented-item',
            option.value === value && 'ui-segmented-item-selected',
          )}
        >
          <span className="ui-segmented-item-label">{option.label}</span>
        </ToggleGroup.Item>
      ))}
    </ToggleGroup.Root>
  )
}) as <T extends string | number = string>(
  props: SegmentedProps<T> & React.RefAttributes<HTMLDivElement>,
) => React.ReactElement
export type SelectOption = {
  value: any
  label?: React.ReactNode
  disabled?: boolean
  [key: string]: any
}
type SelectProps = {
  options?: SelectOption[]
  value?: any
  defaultValue?: any
  onChange?: (value: any, option?: any) => void
  onBlur?: () => void
  placeholder?: React.ReactNode
  disabled?: boolean
  allowClear?: boolean
  showSearch?: boolean
  optionFilterProp?: string
  filterOption?: boolean | ((input: string, option?: SelectOption) => boolean)
  optionRender?: (option: {
    data: SelectOption
    label: React.ReactNode
    value: any
  }) => React.ReactNode
  mode?: 'multiple' | 'tags'
  loading?: boolean
  notFoundContent?: React.ReactNode
  listHeight?: number
  style?: React.CSSProperties
  className?: string
  id?: string
  size?: string
  'aria-label'?: string
  'aria-invalid'?: boolean
  'aria-describedby'?: string
  onSearch?: (input: string) => void
  popupMatchSelectWidth?: boolean
  onDropdownVisibleChange?: (open: boolean) => void
}
export const Select = forwardRef<HTMLButtonElement, SelectProps>(
  function Select(
    {
      options = [],
      value,
      defaultValue,
      onChange,
      onBlur,
      placeholder,
      disabled,
      allowClear,
      showSearch,
      optionFilterProp = 'label',
      filterOption,
      optionRender,
      mode,
      loading,
      notFoundContent = '没有匹配项',
      listHeight = 280,
      style,
      className,
      id,
      size,
      onSearch,
      popupMatchSelectWidth,
      onDropdownVisibleChange,
      ...aria
    },
    ref,
  ) {
    const [open, setOpen] = useState(false)
    const commandId = useId()
    const [search, setSearch] = useState('')
    const [internal, setInternal] = useState(defaultValue)
    const current = value === undefined && !onChange ? internal : value
    const selected = mode
      ? Array.isArray(current)
        ? current
        : []
      : current === undefined ||
          current === null ||
          (current === '' && !options.some((option) => option.value === ''))
        ? []
        : [current]
    const update = (next: any) => {
      setInternal(next)
      onChange?.(
        next,
        mode
          ? options.filter((o) => next?.includes(o.value))
          : options.find((o) => o.value === next),
      )
    }
    const choose = (next: any) => {
      update(
        mode
          ? selected.includes(next)
            ? selected.filter((v) => v !== next)
            : [...selected, next]
          : next,
      )
      setSearch('')
      if (!mode) setOpen(false)
    }
    const availableOptions: SelectOption[] =
      mode === 'tags'
        ? [
            ...options,
            ...selected
              .filter(
                (value) => !options.some((option) => option.value === value),
              )
              .map((value) => ({ value, label: String(value) })),
          ]
        : options
    const filtered = availableOptions.filter(
      (option) =>
        !search ||
        (typeof filterOption === 'function'
          ? filterOption(search, option)
          : filterOption === false
            ? true
            : String(option[optionFilterProp] ?? option.value)
                .toLowerCase()
                .includes(search.toLowerCase())),
    )
    // Cap each rendered page, keeping search across all options to avoid large subscription DOMs.
    const [limit, setLimit] = useState(80)
    useEffect(() => setLimit(80), [search, open])
    return (
      <Popover
        open={open}
        onOpenChange={(next) => {
          setOpen(next)
          onDropdownVisibleChange?.(next)
          if (!next) {
            setSearch('')
            onBlur?.()
          }
        }}
      >
        <div
          className={cn(
            'ui-select',
            mode ? 'ui-select-multiple' : 'ui-select-single',
            disabled && 'ui-select-disabled',
            size === 'small' && 'ui-select-sm',
            className,
          )}
          style={style}
        >
          <PopoverTrigger asChild>
            <button
              {...aria}
              id={id}
              ref={ref}
              type="button"
              role="combobox"
              aria-expanded={open}
              aria-controls={commandId}
              disabled={disabled}
              className="ui-select-selector"
            >
              <span
                className={cn(
                  'ui-select-selection-item',
                  !selected.length && 'ui-select-placeholder',
                )}
              >
                {selected.length
                  ? selected
                      .map(
                        (v) =>
                          options.find((o) => o.value === v)?.label ??
                          String(v),
                      )
                      .map((label, i) => (
                        <span
                          key={i}
                          className={mode ? 'ui-select-tag' : undefined}
                        >
                          {label}
                        </span>
                      ))
                  : placeholder || '请选择'}
              </span>
              {loading ? (
                <LoaderCircle className="size-3.5 animate-spin" />
              ) : (
                <ChevronDown className="size-3.5 shrink-0 opacity-50" />
              )}
            </button>
          </PopoverTrigger>
          {allowClear && selected.length > 0 && !disabled && (
            <button
              type="button"
              aria-label="清空选择"
              className="ui-select-clear"
              onClick={() => update(mode ? [] : undefined)}
            >
              <X size={12} />
            </button>
          )}
        </div>
        <PopoverContent
          className="ui-select-dropdown p-0"
          align="start"
          onOpenAutoFocus={(event) => {
            if (!showSearch && !mode) {
              event.preventDefault()
              document.getElementById(commandId)?.focus()
            }
          }}
          style={{
            width: popupMatchSelectWidth === false ? 'max-content' : 'var(--radix-popover-trigger-width)',
            minWidth: popupMatchSelectWidth === false
              ? 'min(var(--radix-popover-trigger-width), calc(100vw - 24px))'
              : undefined,
          }}
        >
          <Command id={commandId} tabIndex={-1} shouldFilter={false}>
            {(showSearch || mode) && (
              <CommandInput
                placeholder={mode === 'tags' ? '输入或选择…' : '搜索…'}
                value={search}
                onValueChange={(s) => {
                  setSearch(s)
                  onSearch?.(s)
                }}
              />
            )}
            <CommandList
              style={{
                maxHeight: `min(${listHeight}px, calc(var(--radix-popover-content-available-height) - ${showSearch || mode ? 44 : 8}px))`,
              }}
              onScroll={(event) => {
                const el = event.currentTarget
                if (el.scrollHeight - el.scrollTop - el.clientHeight < 50)
                  setLimit((n) => n + 80)
              }}
            >
              {!filtered.length && (
                <CommandEmpty>{notFoundContent}</CommandEmpty>
              )}
              {mode === 'tags' &&
                search.trim() &&
                !availableOptions.some(
                  (o) => String(o.value) === search.trim(),
                ) && (
                  <CommandItem
                    value={`new-${search}`}
                    onSelect={() => choose(search.trim())}
                  >
                    添加「{search.trim()}」
                  </CommandItem>
                )}
              {filtered.slice(0, limit).map((option, index) => (
                <CommandItem
                  key={`${String(option.value)}-${index}`}
                  value={`${String(option.value)}-${index}`}
                  disabled={option.disabled}
                  title={typeof option.title === 'string' ? option.title : undefined}
                  onSelect={() => choose(option.value)}
                  className="ui-select-item"
                >
                  <span className="min-w-0 flex-1">
                    {optionRender
                      ? optionRender({
                          data: option,
                          label: option.label,
                          value: option.value,
                        })
                      : (option.label ?? String(option.value))}
                  </span>
                  {selected.includes(option.value) && (
                    <Check className="size-4 shrink-0" />
                  )}
                </CommandItem>
              ))}
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
    )
  },
)
export const AutoComplete = forwardRef<
  HTMLInputElement,
  Omit<InputProps, 'onChange' | 'value' | 'onSelect'> & {
    options?: SelectOption[]
    value?: string
    onChange?: (value: string) => void
    filterOption?: (input: string, option?: SelectOption) => boolean
  }
>(function AutoComplete(
  { options = [], value = '', onChange, filterOption, ...props },
  ref,
) {
  const [open, setOpen] = useState(false)
  const [activeOption, setActiveOption] = useState(-1)
  const inputRef = useRef<HTMLInputElement | null>(null)
  const id = useId()
  const filtered = options
    .filter((o) => !filterOption || filterOption(value, o))
    .slice(0, 80)
  useEffect(() => setActiveOption(-1), [value])
  useEffect(() => {
    if (!open) setActiveOption(-1)
  }, [open])
  useEffect(() => {
    if (activeOption >= 0)
      document
        .getElementById(`${id}-${activeOption}`)
        ?.scrollIntoView({ block: 'nearest' })
  }, [activeOption, id])
  return (
    <Popover open={open && filtered.length > 0} onOpenChange={setOpen}>
      <PopoverPrimitive.Anchor asChild>
        <div>
          <BaseInput
            {...props}
            ref={(node) => {
              inputRef.current = node
              if (typeof ref === 'function') ref(node)
              else if (ref) ref.current = node
            }}
            role="combobox"
            aria-expanded={open && filtered.length > 0}
            aria-controls={id}
            aria-autocomplete="list"
            aria-activedescendant={
              open && activeOption >= 0 ? `${id}-${activeOption}` : undefined
            }
            value={value}
            onFocus={(event) => {
              props.onFocus?.(event)
              setOpen(true)
            }}
            onKeyDown={(event) => {
              props.onKeyDown?.(event)
              if (event.key === 'Escape' && open) {
                event.preventDefault()
                event.stopPropagation()
                setOpen(false)
              }
              if (
                (event.key === 'ArrowDown' || event.key === 'ArrowUp') &&
                filtered.length
              ) {
                event.preventDefault()
                setOpen(true)
                setActiveOption((index) =>
                  event.key === 'ArrowDown'
                    ? Math.min(index + 1, filtered.length - 1)
                    : Math.max(index - 1, 0),
                )
              }
              if (event.key === 'Enter' && open && filtered[activeOption]) {
                event.preventDefault()
                onChange?.(filtered[activeOption].value)
                setOpen(false)
              }
            }}
            onChange={(event) => {
              onChange?.(event.target.value)
              setOpen(true)
            }}
          />
        </div>
      </PopoverPrimitive.Anchor>
      <PopoverContent
        className="ui-autocomplete-dropdown p-1"
        id={id}
        align="start"
        onOpenAutoFocus={(event) => event.preventDefault()}
        onCloseAutoFocus={(event) => event.preventDefault()}
        style={{ width: 'var(--radix-popover-trigger-width)', minWidth: 200 }}
      >
        <div role="listbox" aria-label={props['aria-label'] || '分组建议'}>
          {filtered.map((option, index) => (
            <button
              type="button"
              id={`${id}-${index}`}
              role="option"
              data-active={index === activeOption || undefined}
              onMouseDown={(event) => event.preventDefault()}
              aria-selected={value === option.value}
              className="ui-autocomplete-option"
              key={option.value}
              onClick={() => {
                onChange?.(option.value)
                setOpen(false)
                inputRef.current?.focus()
              }}
            >
              {option.label || option.value}
            </button>
          ))}
        </div>
      </PopoverContent>
    </Popover>
  )
})
export function Tooltip({
  title,
  children,
  placement,
}: {
  title: React.ReactNode
  children: React.ReactNode
  placement?: string
}) {
  return (
    <TooltipRoot>
      <TooltipTrigger asChild>
        <span className="inline-flex">{children}</span>
      </TooltipTrigger>
      <TooltipContent side={placement === 'bottom' ? 'bottom' : 'top'}>
        {title}
      </TooltipContent>
    </TooltipRoot>
  )
}
function SpaceRoot({
  children,
  className,
  style,
  size,
  direction,
}: {
  children?: React.ReactNode
  className?: string
  style?: React.CSSProperties
  size?: number | string
  direction?: string
}) {
  return (
    <div
      className={cn(
        'ui-space',
        direction === 'vertical' && 'flex-col',
        className,
      )}
      style={{ gap: typeof size === 'number' ? size : 8, ...style }}
    >
      {children}
    </div>
  )
}
export const Space = Object.assign(SpaceRoot, {
  Compact: ({
    children,
    className,
    style,
  }: React.HTMLAttributes<HTMLDivElement>) => (
    <div className={cn('ui-space-compact', className)} style={style}>
      {children}
    </div>
  ),
})
export function useBreakpoint() {
  const read = () => ({
    xs: window.innerWidth < 576,
    sm: window.innerWidth >= 576,
    md: window.innerWidth >= 768,
    lg: window.innerWidth >= 992,
    xl: window.innerWidth >= 1200,
    xxl: window.innerWidth >= 1600,
  })
  const [value, setValue] = useState(read)
  useEffect(() => {
    const update = () => setValue(read())
    window.addEventListener('resize', update)
    return () => window.removeEventListener('resize', update)
  }, [])
  return value
}
export const Grid = { useBreakpoint }
export function Spin({
  size,
  children,
  spinning = true,
}: {
  size?: string
  children?: React.ReactNode
  spinning?: boolean
}) {
  const spinner = (
    <span role="status" aria-label="加载中" className="ui-spin">
      <LoaderCircle
        className={cn('animate-spin', size === 'small' ? 'size-4' : 'size-6')}
      />
    </span>
  )
  return children ? (
    <div className="ui-spin-container" aria-busy={spinning}>
      {children}
      {spinning && <div className="ui-spin-overlay">{spinner}</div>}
    </div>
  ) : spinning ? (
    spinner
  ) : null
}
export function Alert({
  type = 'info',
  message,
  description,
  action,
  showIcon,
  className,
  style,
}: {
  style?: React.CSSProperties
  type?: string
  message?: React.ReactNode
  description?: React.ReactNode
  action?: React.ReactNode
  showIcon?: boolean
  className?: string
}) {
  return (
    <div
      role="alert"
      style={style}
      className={cn('ui-alert', `ui-alert-${type}`, className)}
    >
      {showIcon && <CircleAlert className="size-4 shrink-0" />}
      <div className="min-w-0 flex-1">
        <div>{message}</div>
        {description && (
          <div className="text-muted-foreground text-sm">{description}</div>
        )}
      </div>
      {action}
    </div>
  )
}
function TypographyText({
  children,
  copyable,
  code,
  strong,
  type,
  className,
  style,
  ...props
}: React.HTMLAttributes<HTMLSpanElement> & {
  copyable?: boolean | { text?: string; tooltips?: boolean | string[] }
  code?: boolean
  strong?: boolean
  type?: string
}) {
  const [copied, setCopied] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout>>()
  useEffect(() => () => clearTimeout(timer.current), [])
  return (
    <span
      {...props}
      className={cn(
        'ui-typography',
        strong && 'font-semibold',
        code && 'font-mono',
        type === 'danger' && 'text-destructive',
        className,
      )}
      style={style}
    >
      {children}
      {copyable && (
        <button
          type="button"
          className={cn(
            'ui-copy ui-typography-copy',
            copied && 'ui-typography-copy-success',
          )}
          aria-label={copied ? '已复制' : '复制'}
          onClick={() => {
            copyToClipboard(
              typeof copyable === 'object' && copyable.text !== undefined
                ? copyable.text
                : String(children ?? ''),
            ).then(() => {
              setCopied(true)
              clearTimeout(timer.current)
              timer.current = setTimeout(() => setCopied(false), 1500)
            }).catch(() => message.error('复制失败，请手动选择文本复制'))
          }}
        >
          {copied ? <Check size={14} /> : <Copy size={14} />}
        </button>
      )}
    </span>
  )
}
export const Typography = {
  Text: TypographyText,
  Paragraph: (props: React.ComponentProps<typeof TypographyText>) => (
    <div className="ui-typography-paragraph">
      <TypographyText {...props} />
    </div>
  ),
}
