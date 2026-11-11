import * as RS from '@radix-ui/react-select'
import { Check, ChevronDown } from 'lucide-react'
import { cx } from '../lib/cx'
import { inputClass } from './Input'
import { useField } from './Field'

export interface SelectOption {
  value: string
  label: string
  disabled?: boolean
}

export interface SelectProps {
  value: string
  onValueChange: (v: string) => void
  options: SelectOption[]
  placeholder?: string
  disabled?: boolean
  'aria-label'?: string
  className?: string
}

export function Select({ value, onValueChange, options, placeholder, disabled, className, ...rest }: SelectProps) {
  const f = useField()
  return (
    <RS.Root value={value || undefined} onValueChange={onValueChange} disabled={disabled}>
      <RS.Trigger
        id={f?.id}
        aria-label={rest['aria-label']}
        aria-invalid={f?.invalid || undefined}
        aria-describedby={f?.describedBy}
        className={cx(inputClass, 'flex items-center justify-between gap-2 text-left', className)}
      >
        <RS.Value placeholder={<span className="text-ink-faint">{placeholder}</span>} />
        <RS.Icon>
          <ChevronDown aria-hidden className="size-4 text-ink-muted" />
        </RS.Icon>
      </RS.Trigger>
      <RS.Portal>
        <RS.Content
          position="popper"
          sideOffset={4}
          className="z-50 max-h-72 min-w-[var(--radix-select-trigger-width)] overflow-hidden rounded-md border border-hairline bg-surface shadow-2"
        >
          <RS.Viewport className="p-1">
            {options.map((o) => (
              <RS.Item
                key={o.value}
                value={o.value}
                disabled={o.disabled}
                className="relative flex cursor-default items-center rounded-sm py-2 pr-3 pl-7 text-body outline-none select-none data-[disabled]:text-ink-faint data-[highlighted]:bg-canvas-soft"
              >
                <RS.ItemIndicator className="absolute left-2">
                  <Check aria-hidden className="size-4 text-primary" />
                </RS.ItemIndicator>
                <RS.ItemText>{o.label}</RS.ItemText>
              </RS.Item>
            ))}
          </RS.Viewport>
        </RS.Content>
      </RS.Portal>
    </RS.Root>
  )
}
