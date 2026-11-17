import type { TFunction } from 'i18next'
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

// moving tells whether data is being transferred. Engines that report no
// phase (restic) report only their transfer.
const moving = (p: Progress) => !p.phase || p.phase === 'transfer'

// progressText is the line under the bar. Before the transfer nothing has
// moved yet, so those phases say what tern is doing instead of "0 B". A
// transfer distinguishes confirmed totals from a scan still finding files.
function progressText(t: TFunction, p: Progress): string {
  switch (p.phase) {
    case 'connect':
      return t('progress.connect')
    case 'check':
      return t('progress.check')
    case 'plan': {
      const parts = [t('progress.plan')]
      if (p.scanned > 0) parts.push(t('progress.plan_scanned', { n: p.scanned.toLocaleString() }))
      if (p.files_done > 0) {
        parts.push(
          p.bytes_done > 0
            ? t('progress.plan_found_bytes', { n: p.files_done.toLocaleString(), bytes: formatBytes(p.bytes_done) })
            : t('progress.plan_found', { n: p.files_done.toLocaleString() }),
        )
      }
      return parts.join(' · ')
    }
  }
  const done = formatBytes(p.bytes_done)
  const speed = formatSpeed(p.speed_bps)
  if (p.bytes_total <= 0) return t('progress.transfer_unknown', { done, speed })
  const total = formatBytes(p.bytes_total)
  if (p.total_estimated) return t('progress.transfer_scanning', { done, total, speed })
  return t('progress.transfer', { done, total, speed, eta: formatETA(p.eta_sec) })
}

// RunProgress renders the progress bar and status line for an active run.
// The bar fills only while data moves with a known total; otherwise it
// pulses. showUnit adds the folder pair, for runs with more than one.
export function RunProgress({ progress, showUnit = false }: { progress: Progress; showUnit?: boolean }) {
  const { t } = useTranslation()
  const m = moving(progress)
  return (
    <>
      <ProgressBar done={m ? progress.bytes_done : 0} total={m ? progress.bytes_total : 0} estimated={Boolean(progress.total_estimated)} />
      <p className="text-caption text-ink-muted">{progressText(t, progress)}</p>
      {showUnit && progress.unit ? (
        <p className="truncate text-caption text-ink-muted" title={progress.unit}>
          {progress.unit}
        </p>
      ) : null}
    </>
  )
}
