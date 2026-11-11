import { createContext, useContext, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { TaskCreate } from '../../api/queries'
import type { BackupSpec, Hook, SyncSpec, Task, TaskType } from '../../api/types'
import { Field } from '../../ui/Field'
import { Input } from '../../ui/Input'
import { Textarea } from '../../ui/Textarea'

// Draft is the task being edited: a whole Task when editing, one without the
// server-made fields when new.
export type Draft = TaskCreate & Partial<Pick<Task, 'id' | 'version' | 'created_at' | 'updated_at'>>

const hook = (): Hook => ({ script: '', where: 'local', connection_id: '', timeout_sec: 600, ignore_failure: false })

export const defaultBackup = (): BackupSpec => ({
  connection_id: '',
  repo_path: '',
  host: '',
  sources: [''],
  excludes: [],
  limit_upload_kib: 0,
  connections: 5,
  keep: { last: 0, daily: 7, weekly: 4, monthly: 12 },
  prune_every_days: 7,
  check_every_days: 30,
})

export const defaultSync = (): SyncSpec => ({
  protocol: 'sftp',
  connection_id: '',
  direction: 'push',
  pairs: [{ local: '', remote: '' }],
  delete_mode: 'keep',
  max_deletes: 100,
  excludes: [],
  compare: 'size_mtime',
  limit_kib: 0,
  transfers: 4,
})

export function newDraft(type: TaskType): Draft {
  return {
    type,
    name: '',
    enabled: true,
    schedule: '0 3 * * *',
    catch_up: false,
    stale_after_sec: 0,
    max_runtime_sec: 0,
    retry: { retries: 3, delays_sec: [60, 300, 900] },
    pre: hook(),
    post: hook(),
    ...(type === 'backup' ? { backup: defaultBackup() } : { sync: defaultSync() }),
  }
}

// draftOf copies exactly the fields of a Task; anything else the cache may
// hold (repo_state after a create) stays out, since the server refuses
// unknown fields.
export function draftOf(t: Task): Draft {
  return {
    id: t.id,
    version: t.version,
    type: t.type,
    name: t.name,
    enabled: t.enabled,
    schedule: t.schedule,
    catch_up: t.catch_up,
    stale_after_sec: t.stale_after_sec,
    max_runtime_sec: t.max_runtime_sec,
    retry: { retries: t.retry.retries, delays_sec: [...(t.retry.delays_sec ?? [])] },
    pre: { ...t.pre },
    post: { ...t.post },
    ...(t.backup ? { backup: { ...t.backup, sources: [...(t.backup.sources ?? [])], excludes: [...(t.backup.excludes ?? [])], keep: { ...t.backup.keep } } } : {}),
    ...(t.sync ? { sync: { ...t.sync, pairs: (t.sync.pairs ?? []).map((p) => ({ ...p })), excludes: [...(t.sync.excludes ?? [])] } } : {}),
    created_at: t.created_at,
    updated_at: t.updated_at,
  }
}

// hasNaN reports a number box holding something that is not a number.
export function hasNaN(v: unknown): boolean {
  if (typeof v === 'number') return Number.isNaN(v)
  if (Array.isArray(v)) return v.some(hasNaN)
  if (v && typeof v === 'object') return Object.values(v).some(hasNaN)
  return false
}

// ---- field errors, keyed exactly like the server's `fields` ("sync.pairs[1].local")

const ErrorsCtx = createContext<Record<string, string>>({})
export const ErrorsProvider = ErrorsCtx.Provider

// useFieldError gives the localized error of one server field, if any.
export function useFieldError(): (key: string) => string | undefined {
  const errs = useContext(ErrorsCtx)
  const { t } = useTranslation()
  return (key) => {
    const code = errs[key]
    if (!code) return undefined
    if (key === 'repo_password' && code === 'wrong_repo_password') return t('error.wrong_repo_password')
    if (key === 'name' && code === 'taken') return t('task.name_taken')
    return t(`field.${code}`, { defaultValue: code })
  }
}

// useListErrors collects "prefix[i]" errors as "line i+1: …" text.
export function useListErrors(prefix: string): string | undefined {
  const errs = useContext(ErrorsCtx)
  const { t } = useTranslation()
  const lines = Object.entries(errs)
    .map(([k, code]) => {
      const m = new RegExp(`^${prefix.replace(/[.[\]]/g, '\\$&')}\\[(\\d+)\\]$`).exec(k)
      return m ? { n: Number(m[1]) + 1, code } : null
    })
    .filter((x): x is { n: number; code: string } => x !== null)
    .sort((a, b) => a.n - b.n)
  if (lines.length === 0) return errs[prefix] ? t(`field.${errs[prefix]}`, { defaultValue: errs[prefix] }) : undefined
  return lines.map((l) => t('task.excludes_line', { n: l.n, msg: t(`field.${l.code}`, { defaultValue: l.code }) })).join('；')
}

// ---- inputs

// NumInput edits a number shown in some unit (scale: seconds per unit). Text
// that is not a number reports NaN, which blocks saving.
export function NumInput({
  value,
  onChange,
  scale = 1,
  emptyZero,
  placeholder,
}: {
  value: number
  onChange: (n: number) => void
  scale?: number
  emptyZero?: boolean
  placeholder?: string
}) {
  const show = (v: number) => (emptyZero && v === 0 ? '' : String(Number((v / scale).toFixed(2))))
  const [text, setText] = useState(() => show(value))
  return (
    <Input
      inputMode="decimal"
      value={text}
      placeholder={placeholder}
      onChange={(e) => {
        const s = e.target.value
        setText(s)
        if (s.trim() === '') return onChange(0)
        const n = Number(s.trim())
        onChange(Number.isFinite(n) ? Math.round(n * scale) : Number.NaN)
      }}
    />
  )
}

export function NumField({ label, hint, error, ...rest }: Parameters<typeof NumInput>[0] & { label: string; hint?: string; error?: string }) {
  const { t } = useTranslation()
  const [bad, setBad] = useState(false)
  return (
    <Field label={label} hint={hint} error={bad ? t('field.invalid') : error}>
      <NumInput
        {...rest}
        onChange={(n) => {
          setBad(Number.isNaN(n))
          rest.onChange(n)
        }}
      />
    </Field>
  )
}

// ExcludesField edits a list of rules, one per line.
export function ExcludesField({ prefix, value, onChange }: { prefix: string; value: string[]; onChange: (v: string[]) => void }) {
  const { t } = useTranslation()
  const [text, setText] = useState(() => value.join('\n'))
  const error = useListErrors(prefix)
  return (
    <Field label={t('task.excludes')} hint={t('task.excludes_hint')} error={error}>
      <Textarea
        mono
        rows={4}
        value={text}
        onChange={(e) => {
          setText(e.target.value)
          onChange(
            e.target.value
              .split('\n')
              .map((l) => l.trim())
              .filter(Boolean),
          )
        }}
      />
    </Field>
  )
}
