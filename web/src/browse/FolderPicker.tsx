import { ArrowUp, Folder, FolderOpen, HardDrive, House, Loader2 } from 'lucide-react'
import { useState, type KeyboardEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { ApiError } from '../api/client'
import { errorText } from '../api/errors'
import { useBrowse, type BrowseSource } from '../api/queries'
import { cx } from '../lib/cx'
import { cleanPath, parentPath } from '../lib/paths'
import { Banner } from '../ui/Banner'
import { Button } from '../ui/Button'
import { Dialog } from '../ui/Dialog'
import { Input, type InputProps } from '../ui/Input'

// Where a picker opens when its field is empty. "" lets the server choose
// for tern's own file system (/share on a NAS, / in a container) and is
// the SSH user's home directory on a connection.
function startOf(src: BrowseSource): string {
  switch (src.kind) {
    case 'local':
      return ''
    case 'connection':
      return src.type === 'webdav' ? '/' : ''
    case 'snapshot':
      return src.start || '/'
  }
}

// Paths on tern's own file system and in snapshots are absolute; on a
// connection they may be relative to its starting point.
const absoluteOnly = (src: BrowseSource) => src.kind !== 'connection'

// normalize turns what the user typed into what the server is asked for,
// or null when it cannot be a path there.
function normalize(src: BrowseSource, typed: string): string | null {
  let p = typed.trim()
  if (src.kind === 'connection' && src.type === 'ssh') {
    if (p === '~') p = ''
    else if (p.startsWith('~/')) p = p.slice(2)
  }
  if (p === '') return startOf(src)
  if (absoluteOnly(src) && !p.startsWith('/')) return null
  return cleanPath(p)
}

export interface FolderPickerProps {
  source: BrowseSource
  initial: string // the field's value
  onPick: (path: string) => void
  onClose: () => void
}

// FolderPicker is a dialog for choosing an existing folder. It only reads:
// a folder that does not exist yet is typed into the field instead.
export function FolderPicker({ source, initial, onPick, onClose }: FolderPickerProps) {
  const { t } = useTranslation()
  const [want, setWant] = useState(() => normalize(source, initial) ?? startOf(source))
  const [typed, setTyped] = useState<string>()
  const [typedBad, setTypedBad] = useState(false)
  const q = useBrowse(source, want)

  const listing = q.isPlaceholderData ? undefined : q.data
  const shown = q.data?.path ?? want // the folder on screen, possibly the one before while loading
  const ssh = source.kind === 'connection' && source.type === 'ssh'
  const up = parentPath(shown)
  // The SSH home directory itself cannot be stored as a path (it is "").
  const pickable = listing !== undefined && !q.isError && !(ssh && listing.path === '')

  const go = (p: string) => {
    setTyped(undefined)
    setTypedBad(false)
    setWant(p)
  }
  const goTyped = () => {
    const p = normalize(source, typed ?? display(shown))
    if (p === null) return setTypedBad(true)
    go(p)
  }
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      goTyped()
    }
  }
  function display(p: string) {
    return ssh && p === '' ? '~' : p
  }

  const where =
    source.kind === 'local'
      ? t('browse.where_local')
      : source.kind === 'connection'
        ? t('browse.where_conn', { name: source.name })
        : t('browse.where_snapshot', { id: source.shortId })

  const detail = q.error instanceof ApiError && (q.error.code === 'unreachable' || q.error.code === 'unreadable') ? q.error.message : ''
  const errText = q.error instanceof ApiError && q.error.code === 'validation' ? t('browse.bad_path') : errorText(t, q.error)

  return (
    <Dialog
      open
      onOpenChange={(o) => (o ? undefined : onClose())}
      wide
      title={t('browse.title')}
      description={where}
      footer={
        <>
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button
            variant="primary"
            disabled={!pickable}
            onClick={() => {
              if (!listing) return
              onPick(listing.path)
              onClose()
            }}
          >
            {t('browse.choose')}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-1">
        <div className="flex items-center gap-2">
          <Button aria-label={t('browse.up')} title={t('browse.up')} disabled={up === null} onClick={() => up !== null && go(up)} className="px-2">
            <ArrowUp aria-hidden className="size-4" />
          </Button>
          {ssh ? (
            <>
              <Button aria-label={t('browse.home')} title={t('browse.home')} onClick={() => go('')} className="px-2">
                <House aria-hidden className="size-4" />
              </Button>
              <Button aria-label={t('browse.root')} title={t('browse.root')} onClick={() => go('/')} className="px-2 font-mono">
                /
              </Button>
            </>
          ) : null}
          <Input
            aria-label={t('browse.path')}
            aria-invalid={typedBad || undefined}
            value={typed ?? display(shown)}
            onChange={(e) => {
              setTyped(e.target.value)
              setTypedBad(false)
            }}
            onKeyDown={onKey}
            spellCheck={false}
            className="min-w-0 font-mono"
          />
          <Button onClick={goTyped}>{t('browse.go')}</Button>
        </div>
        {typedBad ? (
          <p className="text-caption text-danger">{t('browse.bad_path')}</p>
        ) : (
          <p className="text-caption text-ink-muted">{t('browse.path_hint')}</p>
        )}
      </div>

      {listing?.missing ? <Banner tone="info">{t('browse.missing', { path: display(want) })}</Banner> : null}
      {q.isError ? (
        <Banner tone="danger" action={<Button onClick={() => void q.refetch()}>{t('common.retry')}</Button>}>
          <p>{errText}</p>
          {detail ? <p className="mt-1 text-caption break-words text-ink-muted">{detail}</p> : null}
        </Banner>
      ) : null}

      <div aria-busy={q.isFetching || undefined} className="relative min-h-40 rounded-md border border-hairline">
        {q.isFetching ? (
          <p role="status" className="flex items-center gap-2 px-3 py-2 text-caption text-ink-muted">
            <Loader2 aria-hidden className="size-4 animate-spin" />
            {t('browse.loading')}
          </p>
        ) : null}
        {q.data && !q.isError ? (
          q.data.entries.length === 0 ? (
            q.isFetching ? null : <p className="px-3 py-6 text-center text-caption text-ink-muted">{t('browse.empty')}</p>
          ) : (
            <div className={cx('max-h-[45vh] overflow-y-auto py-1', q.isPlaceholderData && 'opacity-50')}>
              <ul aria-label={t('browse.folders')}>
                {q.data.entries.map((e) => (
                  <li key={e.path}>
                    <button
                      type="button"
                      disabled={q.isPlaceholderData}
                      onClick={() => go(e.path)}
                      className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-body hover:bg-canvas-soft focus-visible:bg-canvas-soft focus-visible:outline-none"
                    >
                      <Folder aria-hidden className="size-4 shrink-0 text-ink-muted" />
                      <span className="min-w-0 flex-1 truncate">{e.name}</span>
                      {e.mount ? (
                        <span title={t('browse.mount_title')} className="inline-flex items-center gap-1 rounded-sm bg-canvas-soft px-1.5 text-caption text-ink-secondary">
                          <HardDrive aria-hidden className="size-3" />
                          {t('browse.mount')}
                        </span>
                      ) : null}
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          )
        ) : null}
      </div>
      {listing?.truncated ? <p className="text-caption text-ink-muted">{t('browse.truncated', { n: listing.entries.length })}</p> : null}
      {ssh && listing?.path === '' ? <p className="text-caption text-ink-muted">{t('browse.home_hint')}</p> : null}
    </Dialog>
  )
}

export interface PathInputProps extends Omit<InputProps, 'value' | 'onChange'> {
  value: string
  onChange: (path: string) => void
  // onPick receives a picked folder; it defaults to onChange.
  onPick?: (path: string) => void
  // source is where the picker looks; null while it cannot look yet (no
  // connection chosen), with unavailable saying why.
  source: BrowseSource | null
  unavailable?: string
}

// PathInput is a path field: type the path, or pick a folder.
export function PathInput({ value, onChange, onPick, source, unavailable, ...rest }: PathInputProps) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  return (
    <div className="flex items-start gap-2">
      <Input value={value} onChange={(e) => onChange(e.target.value)} spellCheck={false} {...rest} className={cx('min-w-0', rest.className)} />
      <Button
        aria-label={t('browse.open')}
        title={source ? t('browse.open') : unavailable}
        disabled={!source}
        onClick={() => setOpen(true)}
        icon={<FolderOpen aria-hidden className="size-4" />}
        className="shrink-0 px-2"
      />
      {open && source ? <FolderPicker source={source} initial={value} onPick={onPick ?? onChange} onClose={() => setOpen(false)} /> : null}
    </div>
  )
}
