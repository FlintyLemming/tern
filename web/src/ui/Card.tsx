import type { ReactNode } from 'react'
import { cx } from '../lib/cx'

export interface CardProps {
  title?: ReactNode
  actions?: ReactNode
  children?: ReactNode
  className?: string
}

// A card sits on the warm canvas with a hairline; no heavy shadow.
export function Card({ title, actions, children, className }: CardProps) {
  return (
    <section className={cx('rounded-lg border border-hairline bg-surface p-6', className)}>
      {title || actions ? (
        <header className="mb-4 flex items-center justify-between gap-4">
          {title ? <h2 className="text-title">{title}</h2> : <span />}
          {actions}
        </header>
      ) : null}
      {children}
    </section>
  )
}
