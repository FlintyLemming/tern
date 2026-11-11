import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { errorText } from '../../api/errors'
import { fetchRunLog } from '../../api/queries'
import { downloadText } from '../../lib/download'
import { Banner } from '../../ui/Banner'
import { Button } from '../../ui/Button'

export const MAX_LINES = 5000
const POLL_MS = 1000
const CHUNK = 256 << 10

// LogViewer follows a run's log: it asks for what follows next_offset every
// second until the server says eof (the log is complete and the run over).
// Only the last 5000 lines stay on the page; the download has everything.
export function LogViewer({ runId, onEOF }: { runId: string; onEOF?: () => void }) {
  const { t } = useTranslation()
  const [lines, setLines] = useState<string[]>([])
  const [partial, setPartial] = useState('')
  const [folded, setFolded] = useState(false)
  const [error, setError] = useState<string>()
  const [downloading, setDownloading] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const atBottom = useRef(true)
  const onEOFRef = useRef(onEOF)
  onEOFRef.current = onEOF

  useEffect(() => {
    let stopped = false
    let timer: ReturnType<typeof setTimeout> | undefined
    let offset = 0
    let rest = ''
    let total = 0 // lines received so far
    const tick = async () => {
      try {
        const r = await fetchRunLog(runId, offset)
        if (stopped) return
        setError(undefined)
        offset = r.next_offset
        const text = rest + r.data
        const parts = text.split('\n')
        rest = parts.pop() ?? ''
        if (parts.length) {
          total += parts.length
          if (total > MAX_LINES) setFolded(true)
          setLines((old) => {
            const all = old.concat(parts)
            return all.length > MAX_LINES ? all.slice(all.length - MAX_LINES) : all
          })
        }
        setPartial(rest)
        if (r.eof) {
          onEOFRef.current?.()
          return
        }
        timer = setTimeout(() => void tick(), r.data.length >= CHUNK ? 0 : POLL_MS)
      } catch (e) {
        if (stopped) return
        setError(errorText(t, e))
        timer = setTimeout(() => void tick(), POLL_MS * 5)
      }
    }
    void tick()
    return () => {
      stopped = true
      clearTimeout(timer)
    }
  }, [runId, t])

  // follow new lines only while the reader is at the bottom
  useLayoutEffect(() => {
    const el = box.current
    if (el && atBottom.current) el.scrollTop = el.scrollHeight
  }, [lines, partial])

  const download = async () => {
    setDownloading(true)
    try {
      let offset = 0
      let out = ''
      for (;;) {
        const r = await fetchRunLog(runId, offset)
        out += r.data
        offset = r.next_offset
        if (r.eof || r.data === '') break
      }
      downloadText(`tern-run-${runId}.log`, out)
    } catch (e) {
      setError(errorText(t, e))
    } finally {
      setDownloading(false)
    }
  }

  const empty = lines.length === 0 && partial === ''
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <h2 className="text-title">{t('run.log')}</h2>
        <Button onClick={() => void download()} pending={downloading}>
          {t('run.log_download')}
        </Button>
      </div>
      {error ? <Banner tone="danger">{error}</Banner> : null}
      <div
        ref={box}
        onScroll={(e) => {
          const el = e.currentTarget
          atBottom.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 8
        }}
        className="max-h-[60vh] min-h-32 overflow-auto rounded-lg border border-hairline bg-surface px-4 py-3 font-mono text-caption leading-relaxed whitespace-pre text-ink-secondary"
      >
        {folded ? <p className="mb-2 text-ink-faint">{t('run.log_folded')}</p> : null}
        {empty ? <p className="text-ink-faint">{t('run.log_empty')}</p> : null}
        {lines.map((l, i) => (
          <div key={i} data-log-line>
            {l || ' '}
          </div>
        ))}
        {partial ? <div data-log-line>{partial}</div> : null}
      </div>
    </div>
  )
}
