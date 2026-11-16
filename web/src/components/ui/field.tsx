import type { ComponentProps } from 'react'
import { Label } from '@/components/ui/label'
import { cn } from '@/lib/util'

function FieldSet({ className, ...props }: ComponentProps<'fieldset'>) {
  return <fieldset data-slot="field-set" className={cn('flex min-w-0 flex-col gap-4', className)} {...props} />
}

function FieldLegend({ className, ...props }: ComponentProps<'legend'>) {
  return <legend data-slot="field-legend" className={cn('mb-4 text-sm font-medium', className)} {...props} />
}

function FieldGroup({ className, ...props }: ComponentProps<'div'>) {
  return <div data-slot="field-group" className={cn('flex min-w-0 flex-col gap-5', className)} {...props} />
}

function Field({ className, orientation = 'vertical', ...props }: ComponentProps<'div'> & { orientation?: 'vertical' | 'horizontal' }) {
  return <div role="group" data-slot="field" data-orientation={orientation} className={cn('flex min-w-0 gap-2 data-[invalid=true]:text-destructive', orientation === 'horizontal' ? 'flex-row items-center' : 'flex-col', className)} {...props} />
}

function FieldContent({ className, ...props }: ComponentProps<'div'>) {
  return <div data-slot="field-content" className={cn('flex min-w-0 flex-1 flex-col gap-1.5', className)} {...props} />
}

function FieldLabel({ className, ...props }: ComponentProps<typeof Label>) {
  return <Label data-slot="field-label" className={cn('leading-snug', className)} {...props} />
}

function FieldDescription({ className, ...props }: ComponentProps<'p'>) {
  return <p data-slot="field-description" className={cn('text-sm leading-normal text-muted-foreground', className)} {...props} />
}

function FieldError({ className, children, ...props }: ComponentProps<'div'>) {
  if (!children) return null
  return <div role="alert" data-slot="field-error" className={cn('text-sm text-destructive', className)} {...props}>{children}</div>
}

export { Field, FieldContent, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet }
