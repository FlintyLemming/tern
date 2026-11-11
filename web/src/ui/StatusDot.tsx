import { useTranslation } from 'react-i18next'

export const STATUSES = [
  'success',
  'warning',
  'partial',
  'failed',
  'interrupted',
  'canceled',
  'skipped',
  'queued',
  'running',
  'retry_wait',
] as const

export type Status = (typeof STATUSES)[number]

// Sticker colours live only here, on the dots.
const color: Record<Status, string> = {
  success: '--color-accent-green',
  warning: '--color-accent-orange',
  partial: '--color-accent-orange',
  failed: '--color-danger',
  interrupted: '--color-danger',
  canceled: '--color-ink-faint',
  skipped: '--color-ink-faint',
  queued: '--color-accent-sky',
  running: '--color-accent-sky',
  retry_wait: '--color-accent-sky',
}

export function StatusDot({ status, withLabel }: { status: Status; withLabel?: boolean }) {
  const { t } = useTranslation()
  const label = t(`status.${status}`)
  const dot = (
    <span
      data-dot
      aria-hidden={withLabel || undefined}
      role={withLabel ? undefined : 'img'}
      aria-label={withLabel ? undefined : label}
      className="inline-block size-2 shrink-0 rounded-full"
      style={{ backgroundColor: `var(${color[status] ?? '--color-ink-faint'})` }}
    />
  )
  if (!withLabel) return dot
  return (
    <span className="inline-flex items-center gap-2">
      {dot}
      <span>{label}</span>
    </span>
  )
}
