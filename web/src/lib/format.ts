// Formatting shared by every page. Sizes are binary (KiB, MiB…) because that is
// what restic, rclone and rsync report.

const UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']

export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  let i = 0
  let v = n
  while (v >= 1024 && i < UNITS.length - 1) {
    v /= 1024
    i++
  }
  if (i === 0) return `${Math.round(v)} B`
  const s = v >= 100 ? String(Math.round(v)) : String(Math.round(v * 10) / 10)
  return `${s} ${UNITS[i]}`
}

export function formatSpeed(bps: number): string {
  return `${formatBytes(bps)}/s`
}

const pad = (n: number) => String(n).padStart(2, '0')

export function formatETA(sec: number): string {
  if (!Number.isFinite(sec) || sec <= 0) return '—'
  const s = Math.round(sec)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const r = s % 60
  return h > 0 ? `${h}:${pad(m)}:${pad(r)}` : `${m}:${pad(r)}`
}

export type Lang = 'zh' | 'en'

const DURATION_UNITS: Record<Lang, [number, string][]> = {
  zh: [
    [86400, ' 天'],
    [3600, ' 小时'],
    [60, ' 分'],
    [1, ' 秒'],
  ],
  en: [
    [86400, 'd'],
    [3600, 'h'],
    [60, 'm'],
    [1, 's'],
  ],
}

// formatDuration gives the two largest non-zero units: "1 小时 2 分" / "1h 2m".
export function formatDuration(sec: number, lang: Lang = 'zh'): string {
  const units = DURATION_UNITS[lang]
  let s = Math.max(0, Math.round(sec))
  if (s === 0) return `0${units[3][1]}`
  const parts: string[] = []
  for (const [size, label] of units) {
    if (s >= size && parts.length < 2) {
      parts.push(`${Math.floor(s / size)}${label}`)
      s %= size
    } else if (parts.length > 0) {
      break // only adjacent units: "1 天 1 小时", never "1 天 5 秒"
    }
  }
  return parts.join(' ')
}

function zoneOrUTC(timeZone: string): string {
  try {
    new Intl.DateTimeFormat('en', { timeZone })
    return timeZone
  } catch {
    return 'UTC'
  }
}

// formatTime renders an RFC 3339 instant as "YYYY-MM-DD HH:mm" in the given
// IANA zone — the node's, never the browser's. An unknown zone falls back to UTC.
export function formatTime(iso: string, timeZone: string, locale: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const parts = new Intl.DateTimeFormat(locale === 'zh' ? 'zh-CN' : 'en-GB', {
    timeZone: zoneOrUTC(timeZone),
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
    numberingSystem: 'latn',
  }).formatToParts(d)
  const get = (type: string) => parts.find((p) => p.type === type)?.value ?? ''
  return `${get('year')}-${get('month')}-${get('day')} ${get('hour')}:${get('minute')}`
}

// zoneLabel is the zone name shown next to times; an unknown zone shows as UTC.
export function zoneLabel(timeZone: string): string {
  return zoneOrUTC(timeZone)
}
