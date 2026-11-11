import { AlertTriangle, Info } from 'lucide-react'
import type { ReactNode } from 'react'
import { cx } from '../lib/cx'

export function Banner({ tone, children, action }: { tone: 'danger' | 'info'; children: ReactNode; action?: ReactNode }) {
  const Icon = tone === 'danger' ? AlertTriangle : Info
  return (
    <div
      role={tone === 'danger' ? 'alert' : 'status'}
      className={cx(
        'flex items-start gap-3 rounded-lg border bg-surface px-4 py-3 text-body',
        tone === 'danger' ? 'border-danger' : 'border-hairline',
      )}
    >
      <Icon aria-hidden className={cx('mt-0.5 size-4 shrink-0', tone === 'danger' ? 'text-danger' : 'text-ink-muted')} />
      <div className="min-w-0 flex-1">{children}</div>
      {action}
    </div>
  )
}
