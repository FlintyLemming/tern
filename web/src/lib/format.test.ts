import { formatBytes, formatDuration, formatETA, formatSpeed, formatTime } from './format'

it('formatBytes', () => {
  expect(formatBytes(0)).toBe('0 B')
  expect(formatBytes(500)).toBe('500 B')
  expect(formatBytes(1536)).toBe('1.5 KiB')
  expect(formatBytes(1048576)).toBe('1 MiB')
  expect(formatBytes(4194304)).toBe('4 MiB')
  expect(formatBytes(1.25 * 1024 ** 3)).toBe('1.3 GiB')
})

it('formatSpeed', () => {
  expect(formatSpeed(2097152)).toBe('2 MiB/s')
  expect(formatSpeed(0)).toBe('0 B/s')
})

it('formatETA', () => {
  expect(formatETA(59)).toBe('0:59')
  expect(formatETA(90)).toBe('1:30')
  expect(formatETA(3725)).toBe('1:02:05')
  expect(formatETA(0)).toBe('—')
  expect(formatETA(-5)).toBe('—')
})

it('formatDuration', () => {
  expect(formatDuration(45)).toBe('45 秒')
  expect(formatDuration(45, 'en')).toBe('45s')
  expect(formatDuration(3725)).toBe('1 小时 2 分')
  expect(formatDuration(3725, 'en')).toBe('1h 2m')
  expect(formatDuration(125, 'en')).toBe('2m 5s')
  expect(formatDuration(90000, 'en')).toBe('1d 1h')
  expect(formatDuration(0, 'en')).toBe('0s')
})

it('formatTime uses the given zone', () => {
  const s = formatTime('2026-11-10T19:00:00Z', 'Asia/Shanghai', 'zh')
  expect(s).toContain('03:00')
  expect(s).toContain('2026-11-11')
  expect(formatTime('2026-11-10T19:00:00Z', 'America/New_York', 'en')).toContain('2026-11-10 14:00')
})

it('formatTime falls back to UTC for a zone the browser does not know', () => {
  expect(formatTime('2026-11-10T19:00:00Z', 'Local', 'en')).toContain('2026-11-10 19:00')
})

it('formatRelative', async () => {
  const { formatRelative } = await import('./format')
  const now = Date.parse('2026-11-11T12:00:00Z')
  expect(formatRelative('2026-11-11T11:59:40Z', now, 'zh')).toBe('刚刚')
  expect(formatRelative('2026-11-11T11:57:00Z', now, 'zh')).toBe('3 分钟前')
  expect(formatRelative('2026-11-11T10:00:00Z', now, 'en')).toBe('2 h ago')
  expect(formatRelative('2026-11-06T12:00:00Z', now, 'zh')).toBe('5 天前')
  expect(formatRelative('2026-11-06T12:00:00Z', now, 'en')).toBe('5 d ago')
})
