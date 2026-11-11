import * as RSw from '@radix-ui/react-switch'
import { useField } from './Field'

export interface SwitchProps {
  checked: boolean
  onCheckedChange: (v: boolean) => void
  disabled?: boolean
  'aria-label'?: string
}

export function Switch({ checked, onCheckedChange, disabled, ...rest }: SwitchProps) {
  const f = useField()
  return (
    <RSw.Root
      id={f?.id}
      checked={checked}
      onCheckedChange={onCheckedChange}
      disabled={disabled}
      aria-label={rest['aria-label']}
      className="relative inline-flex h-5 w-9 shrink-0 items-center rounded-full bg-hairline transition-colors disabled:opacity-50 data-[state=checked]:bg-primary"
    >
      <RSw.Thumb className="block size-4 translate-x-0.5 rounded-full bg-surface shadow-1 transition-transform data-[state=checked]:translate-x-[18px]" />
    </RSw.Root>
  )
}
