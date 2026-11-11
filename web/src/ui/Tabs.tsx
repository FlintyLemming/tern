import * as RTabs from '@radix-ui/react-tabs'
import type { ReactNode } from 'react'

export interface TabItem {
  value: string
  label: ReactNode
  content: ReactNode
}

export interface TabsProps {
  items: TabItem[]
  value?: string
  defaultValue?: string
  onValueChange?: (v: string) => void
}

export function Tabs({ items, value, defaultValue, onValueChange }: TabsProps) {
  return (
    <RTabs.Root value={value} defaultValue={defaultValue ?? items[0]?.value} onValueChange={onValueChange}>
      <RTabs.List className="mb-4 flex gap-1 border-b border-hairline">
        {items.map((it) => (
          <RTabs.Trigger
            key={it.value}
            value={it.value}
            className="-mb-px border-b-2 border-transparent px-3 py-2 text-body text-ink-muted data-[state=active]:border-primary data-[state=active]:text-ink"
          >
            {it.label}
          </RTabs.Trigger>
        ))}
      </RTabs.List>
      {items.map((it) => (
        <RTabs.Content key={it.value} value={it.value} className="focus:outline-none">
          {it.content}
        </RTabs.Content>
      ))}
    </RTabs.Root>
  )
}
