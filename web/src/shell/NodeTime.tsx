import { useTranslation } from 'react-i18next'
import { useSystem } from '../api/queries'
import { formatTime, zoneLabel } from '../lib/format'

// useNodeZone is the node's time zone, or undefined until it is known. Every
// time on screen is shown in it; nothing is shown in the browser's zone, not
// even for a moment.
export function useNodeZone(): string | undefined {
  const sys = useSystem()
  return sys.data ? zoneLabel(sys.data.timezone) : undefined
}

export function useFormatTime(): (iso: string) => string {
  const zone = useNodeZone()
  const { i18n } = useTranslation()
  return (iso) => (zone ? formatTime(iso, zone, i18n.language) : '…')
}

export function NodeTime({ iso, title }: { iso: string | null | undefined; title?: string }) {
  const fmt = useFormatTime()
  if (!iso) return <span>—</span>
  return (
    <time dateTime={iso} title={title}>
      {fmt(iso)}
    </time>
  )
}
