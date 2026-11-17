import { Plus, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import type { BackupSpec, Connection } from '../../api/types'
import { PathInput } from '../../browse/FolderPicker'
import { Button } from '../../ui/Button'
import { Field } from '../../ui/Field'
import { Input, SecretInput } from '../../ui/Input'
import { Select } from '../../ui/Select'
import { ExcludesField, NumField, useFieldError } from './form'

export type RepoPasswordMode = 'generate' | 'own'

export interface RepoPasswordProps {
  mode: RepoPasswordMode
  setMode: (m: RepoPasswordMode) => void
  value: string
  setValue: (v: string) => void
  regenerate: () => void
  generating: boolean
}

export function BackupFields({
  b,
  set,
  sshConns,
  repoPassword,
}: {
  b: BackupSpec
  set: (p: Partial<BackupSpec>) => void
  sshConns: Connection[]
  repoPassword?: RepoPasswordProps // only while creating
}) {
  const { t } = useTranslation()
  const fe = useFieldError()
  const sources = b.sources.length ? b.sources : ['']
  const repoConn = sshConns.find((c) => c.id === b.connection_id)
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <p className="text-eyebrow text-ink-secondary">{t('task.sources')}</p>
        {sources.map((src, i) => (
          <div key={i} data-testid="source" className="flex items-start gap-2">
            <div className="flex-1">
              <Field label={t('task.source')} error={fe(`backup.sources[${i}]`)} className="flex flex-col gap-1 [&>label]:sr-only">
                <PathInput
                  value={src}
                  placeholder={t('task.local_placeholder')}
                  source={{ kind: 'local' }}
                  onChange={(v) => set({ sources: sources.map((s, j) => (j === i ? v : s)) })}
                />
              </Field>
            </div>
            <Button aria-label={t('common.remove')} disabled={sources.length === 1} onClick={() => set({ sources: sources.filter((_, j) => j !== i) })} className="mt-0.5">
              <X aria-hidden className="size-4" />
            </Button>
          </div>
        ))}
        {fe('backup.sources') ? <p className="text-caption text-danger">{fe('backup.sources')}</p> : null}
        <Button icon={<Plus aria-hidden className="size-4" />} onClick={() => set({ sources: [...sources, ''] })} className="self-start">
          {t('task.add_source')}
        </Button>
      </div>

      <ExcludesField prefix="backup.excludes" value={b.excludes} onChange={(excludes) => set({ excludes })} />

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label={t('task.repo_conn')} hint={t('task.repo_conn_hint')} error={fe('backup.connection_id')}>
          <Select
            value={b.connection_id}
            onValueChange={(v) => set({ connection_id: v })}
            placeholder={t('task.conn_placeholder')}
            options={sshConns.map((c) => ({ value: c.id, label: c.name }))}
          />
        </Field>
        <Field label={t('task.repo_path')} hint={t('task.repo_path_hint')} error={fe('backup.repo_path')}>
          <PathInput
            value={b.repo_path}
            placeholder="/srv/backup/…"
            source={repoConn ? { kind: 'connection', id: repoConn.id, type: 'ssh', name: repoConn.name } : null}
            unavailable={t('browse.need_conn')}
            onChange={(v) => set({ repo_path: v })}
          />
        </Field>
      </div>

      {repoPassword ? <RepoPasswordField {...repoPassword} /> : null}

      <div>
        <p className="mb-2 text-eyebrow text-ink-secondary">{t('task.keep')}</p>
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
          {(['last', 'daily', 'weekly', 'monthly'] as const).map((k) => (
            <NumField
              key={k}
              label={t(`task.keep_${k}`)}
              error={fe(`backup.keep.${k}`)}
              value={b.keep[k]}
              onChange={(n) => set({ keep: { ...b.keep, [k]: n } })}
            />
          ))}
        </div>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <NumField
          label={t('task.limit_upload')}
          hint={t('task.limit_hint')}
          error={fe('backup.limit_upload_kib')}
          value={b.limit_upload_kib}
          emptyZero
          onChange={(n) => set({ limit_upload_kib: n })}
        />
        <NumField label={t('task.connections')} error={fe('backup.connections')} value={b.connections} onChange={(n) => set({ connections: n })} />
        <NumField
          label={t('task.prune_every')}
          hint={t('task.period_hint')}
          error={fe('backup.prune_every_days')}
          value={b.prune_every_days}
          onChange={(n) => set({ prune_every_days: n })}
        />
        <NumField
          label={t('task.check_every')}
          hint={t('task.period_hint')}
          error={fe('backup.check_every_days')}
          value={b.check_every_days}
          onChange={(n) => set({ check_every_days: n })}
        />
      </div>
    </div>
  )
}

function RepoPasswordField({ mode, setMode, value, setValue, regenerate, generating }: RepoPasswordProps) {
  const { t } = useTranslation()
  const fe = useFieldError()
  return (
    <div className="flex flex-col gap-2">
      <div role="radiogroup" aria-label={t('task.repo_password_source')} className="flex flex-wrap gap-4">
        {(['generate', 'own'] as const).map((m) => (
          <label key={m} className="inline-flex items-center gap-2">
            <input type="radio" name="repo_password_mode" checked={mode === m} onChange={() => setMode(m)} className="accent-[var(--color-primary)]" />
            {t(m === 'generate' ? 'task.repo_generate' : 'task.repo_own')}
          </label>
        ))}
      </div>
      <div className="flex items-start gap-2">
        <div className="flex-1">
          <Field label={t('task.repo_password')} hint={t('task.repo_password_hint')} error={fe('repo_password')}>
            {mode === 'generate' ? (
              <Input value={value} readOnly className="font-mono" autoComplete="off" spellCheck={false} />
            ) : (
              <SecretInput value={value} onChange={(e) => setValue(e.target.value)} />
            )}
          </Field>
        </div>
        {mode === 'generate' ? (
          <Button onClick={regenerate} pending={generating} className="mt-5">
            {t('task.repo_regenerate')}
          </Button>
        ) : null}
      </div>
    </div>
  )
}
