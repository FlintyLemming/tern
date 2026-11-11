import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { ApiError } from '../../api/client'
import { errorText, fieldCodes, fieldText } from '../../api/errors'
import { useRestore } from '../../api/queries'
import type { Snapshot } from '../../api/types'
import { shQuote } from '../../lib/recovery'
import { Banner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { CodeBlock } from '../../ui/CodeBlock'
import { Dialog } from '../../ui/Dialog'
import { Field } from '../../ui/Field'
import { Input } from '../../ui/Input'

// restoreCommand is what restic would be run with — shown so the restore can
// be repeated by hand without tern.
export function restoreCommand(repository: string, snapshotId: string, subpath: string, target: string): string {
  const what = subpath ? `${snapshotId}:${subpath}` : snapshotId
  return `restic -r ${shQuote(repository)} restore ${shQuote(what)} --target ${target ? shQuote(target) : '…'}`
}

export function RestoreDialog({ taskId, repository, snap, onClose }: { taskId: string; repository: string; snap: Snapshot | null; onClose: () => void }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const restore = useRestore()
  const [subpath, setSubpath] = useState('')
  const [target, setTarget] = useState('')
  const [required, setRequired] = useState(false)
  const [shown, setShown] = useState<string>()
  if (snap && shown !== snap.id) {
    setShown(snap.id)
    setSubpath('')
    setTarget('')
    setRequired(false)
    restore.reset()
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!snap || restore.isPending) return
    if (!target.trim()) return setRequired(true)
    setRequired(false)
    restore.mutate({ id: taskId, snapshot_id: snap.id, subpath: subpath.trim() || undefined, target_dir: target.trim() }, { onSuccess: (r) => navigate(`/runs/${r.run_id}`) })
  }

  const fields = fieldCodes(restore.error)
  const code = restore.error instanceof ApiError ? restore.error.code : ''
  return (
    <Dialog
      open={Boolean(snap)}
      onOpenChange={(o) => {
        if (!o) {
          setShown(undefined)
          onClose()
        }
      }}
      wide
      title={t('snap.restore_title', { id: snap?.short_id })}
      description={t('snap.restore_desc')}
    >
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        <Field label={t('snap.subpath')} hint={t('snap.subpath_hint')} error={fieldText(t, fields.subpath ?? fields.snapshot_id)}>
          <Input value={subpath} onChange={(e) => setSubpath(e.target.value)} spellCheck={false} />
        </Field>
        <Field label={t('snap.target')} error={required ? t('v.required') : fieldText(t, fields.target_dir)}>
          <Input value={target} onChange={(e) => setTarget(e.target.value)} spellCheck={false} placeholder={`/targets/restore-${snap?.short_id ?? ''}`} />
        </Field>
        <div className="flex flex-col gap-1">
          <p className="text-eyebrow text-ink-secondary">{t('snap.command')}</p>
          <CodeBlock text={restoreCommand(repository, snap?.id ?? '', subpath.trim(), target.trim())} copyable />
        </div>
        {restore.isError && (code !== 'validation' || Object.keys(fields).length === 0) ? <Banner tone="danger">{errorText(t, restore.error)}</Banner> : null}
        <div className="flex justify-end">
          <Button variant="primary" type="submit" pending={restore.isPending}>
            {t('snap.start')}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
