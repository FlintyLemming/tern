import type { InputHTMLAttributes, Ref } from 'react'
import { cx } from '../lib/cx'
import { useField } from './Field'

// The input look: 4px radius (never a pill), light grey border, Level 1 shadow on focus.
export const inputClass = cx(
  'w-full rounded-xs border border-input-border bg-surface px-[6px] py-[6px] text-body text-ink',
  'placeholder:text-ink-faint focus:border-primary focus:shadow-1 focus:outline-none',
  'aria-invalid:border-danger disabled:bg-canvas-soft disabled:text-ink-muted',
)

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  ref?: Ref<HTMLInputElement>
}

export function Input({ className, id, ...rest }: InputProps) {
  const f = useField()
  return (
    <input
      id={id ?? f?.id}
      aria-invalid={f?.invalid || rest['aria-invalid'] || undefined}
      aria-describedby={f?.describedBy}
      className={cx(inputClass, className)}
      {...rest}
    />
  )
}

// SecretInput is for credentials: never autocompleted, never shown.
export function SecretInput(props: Omit<InputProps, 'type' | 'autoComplete'>) {
  return <Input type="password" autoComplete="off" spellCheck={false} {...props} />
}
