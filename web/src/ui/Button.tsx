import type { ButtonHTMLAttributes, MouseEvent, ReactNode, Ref } from 'react'
import { Loader2 } from 'lucide-react'
import { cx } from '../lib/cx'

export type ButtonVariant = 'primary' | 'utility' | 'danger'

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant
  // pending: a request is in flight. The button is disabled until it settles,
  // so a double click on a slow network sends one request, not two.
  pending?: boolean
  icon?: ReactNode
  ref?: Ref<HTMLButtonElement>
}

const variants: Record<ButtonVariant, string> = {
  // the one blue action of a view: pill, pressed state darker
  primary: 'rounded-full bg-primary px-5 py-2 text-[16px] font-medium text-on-primary active:bg-primary-active',
  utility: 'rounded-md border border-hairline bg-surface px-[14px] py-1 text-body font-medium text-ink active:bg-canvas-soft',
  danger: 'rounded-md bg-danger px-[14px] py-1 text-body font-medium text-white active:opacity-90',
}

export function Button({ variant = 'utility', pending, disabled, icon, className, children, onClick, type, ...rest }: ButtonProps) {
  const off = disabled || pending
  return (
    <button
      type={type ?? 'button'}
      disabled={off}
      aria-busy={pending || undefined}
      className={cx(
        'inline-flex items-center justify-center gap-2 whitespace-nowrap transition-colors select-none',
        'disabled:cursor-not-allowed disabled:opacity-50',
        variants[variant],
        className,
      )}
      onClick={(e: MouseEvent<HTMLButtonElement>) => {
        if (off) return
        onClick?.(e)
      }}
      {...rest}
    >
      {pending ? <Loader2 aria-hidden className="size-4 animate-spin" /> : icon}
      {children}
    </button>
  )
}
