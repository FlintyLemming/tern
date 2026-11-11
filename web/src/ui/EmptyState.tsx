import type { ReactNode } from 'react'

export function EmptyState({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-4 rounded-xl border border-dashed border-hairline p-8 text-center text-[16px] text-ink-muted">
      <div>{children}</div>
      {action}
    </div>
  )
}
