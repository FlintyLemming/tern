import { useTranslation } from 'react-i18next'
import type { Progress } from '../api/types'
import { formatBytes, formatETA, formatSpeed } from '../lib/format'

export function ProgressBar({ done, total, estimated = false }: { done: number; total: number; estimated?: boolean }) {
  const known = total > 0 && !estimated
  const pct = known ? Math.min(100, Math.max(0, (done / total) * 100)) : 0
  return (
    <div
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={known ? total : undefined}
      aria-valuenow={known ? done : undefined}
      className="h-1.5 w-full overflow-hidden rounded-full bg-hairline"
    >
      <div className={known ? 'h-full bg-primary transition-[width]' : 'h-full w-1/3 animate-pulse bg-primary'} style={known ? { width: `${pct}%` } : undefined} />
    </div>
  )
}

// RunProgress renders the progress bar and status line for an active run,
// distinguishing confirmed totals from incremental scans whose full size is
// not known upfront.
export function RunProgress({ progress }: { progress: Progress }) {
  const { t } = useTranslation()
  const estimated = Boolean(progress.total_estimated)
  const hasTotal = progress.bytes_total > 0
  const text = !hasTotal
    ? t('overview.progress_unknown', { done: formatBytes(progress.bytes_done), speed: formatSpeed(progress.speed_bps) })
    : estimated
      ? t('overview.progress_scanning', { done: formatBytes(progress.bytes_done), total: formatBytes(progress.bytes_total), speed: formatSpeed(progress.speed_bps) })
      : t('overview.progress', {
          done: formatBytes(progress.bytes_done),
          total: formatBytes(progress.bytes_total),
          speed: formatSpeed(progress.speed_bps),
          eta: formatETA(progress.eta_sec),
        })
  return (
    <>
      <ProgressBar done={progress.bytes_done} total={progress.bytes_total} estimated={estimated} />
      <p className="text-caption text-ink-muted">{text}</p>
    </>
  )
}
