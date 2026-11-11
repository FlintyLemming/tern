import type { Ref, TextareaHTMLAttributes } from 'react'
import { cx } from '../lib/cx'
import { inputClass } from './Input'
import { useField } from './Field'

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  mono?: boolean
  ref?: Ref<HTMLTextAreaElement>
}

export function Textarea({ className, mono, id, ...rest }: TextareaProps) {
  const f = useField()
  return (
    <textarea
      id={id ?? f?.id}
      aria-invalid={f?.invalid || rest['aria-invalid'] || undefined}
      aria-describedby={f?.describedBy}
      spellCheck={mono ? false : undefined}
      className={cx(inputClass, 'min-h-24 resize-y', mono && 'font-mono text-caption', className)}
      {...rest}
    />
  )
}
