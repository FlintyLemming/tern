import { useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate, useParams, useSearchParams } from 'react-router'
import { ApiError } from '../../api/client'
import { errorText } from '../../api/errors'
import { generateRepoPassword, useConnections, useDeleteTask, useSaveTask, useTask } from '../../api/queries'
import type { Task, TaskType } from '../../api/types'
import { recoveryRepository } from '../../lib/recovery'
import { Banner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { Card } from '../../ui/Card'
import { Dialog } from '../../ui/Dialog'
import { Field } from '../../ui/Field'
import { Input } from '../../ui/Input'
import { PageHeader } from '../../ui/PageHeader'
import { Switch } from '../../ui/Switch'
import { useToast } from '../../ui/Toast'
import { BackupFields, type RepoPasswordMode } from './BackupFields'
import { draftOf, ErrorsProvider, hasNaN, newDraft, useFieldError, type Draft } from './form'
import { HookFields } from './HookFields'
import { RecoveryDialog } from './RecoveryDialog'
import { ScheduleField } from './ScheduleField'
import { SyncFields } from './SyncFields'

export interface EditorExtrasProps {
  task: Task
}

// TaskEditor is the page for /tasks/new?type=… and /tasks/:id. Another task
// (or type) is another form: nothing typed for one carries over to the next.
export function TaskEditor({ extras }: { extras?: (p: EditorExtrasProps) => ReactNode }) {
  const { id } = useParams()
  const [params] = useSearchParams()
  return <TaskEditorFor key={id ?? `new-${params.get('type')}`} extras={extras} />
}

function TaskEditorFor({ extras }: { extras?: (p: EditorExtrasProps) => ReactNode }) {
  const { id } = useParams()
  const [params] = useSearchParams()
  const { t } = useTranslation()
  const task = useTask(id)
  const [draft, setDraft] = useState<Draft | null>(() => (id ? null : newDraft(params.get('type') === 'backup' ? 'backup' : 'sync')))
  const [formKey, setFormKey] = useState(0)

  // The form is filled once from the server; later refetches (another tab
  // saving, an event) must not overwrite what is being typed.
  useEffect(() => {
    if (id && task.data && draft === null) setDraft(draftOf(task.data))
  }, [id, task.data, draft])

  if (id && task.isError) {
    return (
      <>
        <PageHeader title={t('task.edit')} />
        <Banner tone="danger">{task.error instanceof ApiError && task.error.status === 404 ? t('task.not_found') : errorText(t, task.error)}</Banner>
      </>
    )
  }
  if (!draft) return <p className="text-caption text-ink-muted">{t('common.loading')}</p>

  const reload = async () => {
    const r = await task.refetch()
    if (r.data) {
      setDraft(draftOf(r.data))
      setFormKey((k) => k + 1)
    }
  }

  return <EditorForm key={formKey} draft={draft} setDraft={setDraft} onReload={reload} extras={task.data && extras ? extras({ task: task.data }) : null} />
}

function EditorForm({
  draft: d,
  setDraft,
  onReload,
  extras,
}: {
  draft: Draft
  setDraft: (f: (d: Draft | null) => Draft | null) => void
  onReload: () => Promise<void>
  extras: ReactNode
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const toast = useToast()
  const conns = useConnections()
  const save = useSaveTask()
  const del = useDeleteTask()
  const creating = !d.id
  const type: TaskType = d.type
  const sshConns = useMemo(() => (conns.data ?? []).filter((c) => c.type === 'ssh'), [conns.data])

  const [errors, setErrors] = useState<Record<string, string>>({})
  const [general, setGeneral] = useState<string>()
  const [conflict, setConflict] = useState(false)
  const [scheduleInvalid, setScheduleInvalid] = useState(false)
  const [recoveryOpen, setRecoveryOpen] = useState(false)
  const [deleting, setDeleting] = useState(false)

  // repository password: only while creating a backup task
  const [pwMode, setPwMode] = useState<RepoPasswordMode>('generate')
  const [genPw, setGenPw] = useState('')
  const [ownPw, setOwnPw] = useState('')
  const [generating, setGenerating] = useState(false)
  const wantsGenerated = creating && type === 'backup'
  const asked = useRef(false)
  const regenerate = () => {
    setGenerating(true)
    generateRepoPassword()
      .then((r) => setGenPw(r.password))
      .catch((e: unknown) => setGeneral(errorText(t, e)))
      .finally(() => setGenerating(false))
  }
  const regenerateRef = useRef(regenerate)
  regenerateRef.current = regenerate
  useEffect(() => {
    if (wantsGenerated && !asked.current) {
      asked.current = true
      regenerateRef.current()
    }
  }, [wantsGenerated])
  const repoPassword = pwMode === 'generate' ? genPw : ownPw

  const patch = (p: Partial<Draft>) => setDraft((x) => (x ? { ...x, ...p } : x))

  const body = (): Draft => {
    // exactly the fields of a task: the server refuses anything else
    const out: Draft = { ...d }
    if (out.sync?.protocol === 'rsync') out.sync = { ...out.sync, transfers: 0 }
    if (type === 'backup') delete out.sync
    else delete out.backup
    return out
  }

  const fail = (e: unknown) => {
    setRecoveryOpen(false)
    if (!(e instanceof ApiError)) return setGeneral(errorText(t, e))
    if (e.code === 'validation' && e.fields) {
      setErrors(e.fields)
      setGeneral(t('error.validation'))
    } else if (e.code === 'wrong_repo_password') {
      setErrors({ repo_password: 'wrong_repo_password' })
      setGeneral(undefined)
    } else if (e.code === 'conflict' && (creating || e.message.includes('already has this name'))) {
      setErrors({ name: 'taken' })
      setGeneral(undefined)
    } else if (e.code === 'conflict') {
      setConflict(true)
      setGeneral(undefined)
    } else {
      setGeneral(errorText(t, e))
    }
  }

  const send = (extra: { repo_password?: string; recovery_confirmed?: boolean } = {}) => {
    if (save.isPending) return
    setErrors({})
    setGeneral(undefined)
    save.mutate(
      { id: d.id, task: { ...body(), ...extra } },
      {
        onSuccess: (r) => {
          setOwnPw('')
          setGenPw('')
          setRecoveryOpen(false)
          if (r.repo_state === 'new') toast(t('task.repo_new'))
          else if (r.repo_state === 'existing') toast(t('task.repo_existing'))
          else toast(t('task.saved'))
          navigate('/tasks')
        },
        onError: fail,
      },
    )
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (save.isPending || scheduleInvalid) return
    if (hasNaN(d)) return setGeneral(t('error.validation'))
    if (creating && type === 'backup') {
      // nothing is sent before the recovery information has been kept
      const local: Record<string, string> = {}
      if (!repoPassword) local.repo_password = 'required'
      if (!d.backup?.connection_id) local['backup.connection_id'] = 'required'
      if (!d.backup?.repo_path) local['backup.repo_path'] = 'required'
      setErrors(local)
      if (Object.keys(local).length) return setGeneral(t('error.validation'))
      setGeneral(undefined)
      setRecoveryOpen(true)
      return
    }
    send()
  }

  const repoConn = sshConns.find((c) => c.id === d.backup?.connection_id)
  const title = creating ? t(type === 'backup' ? 'task.new_backup' : 'task.new_sync') : t('task.edit')

  return (
    <ErrorsProvider value={errors}>
      <PageHeader
        title={title}
        description={d.name || undefined}
        actions={
          creating ? null : (
            <Button variant="danger" onClick={() => setDeleting(true)}>
              {t('task.delete')}
            </Button>
          )
        }
      />
      <form onSubmit={submit} className="flex flex-col gap-6 pb-24" noValidate>
        {conflict ? (
          <Banner
            tone="danger"
            action={
              <Button
                onClick={() => {
                  setConflict(false)
                  void onReload()
                }}
              >
                {t('common.reload')}
              </Button>
            }
          >
            <p className="font-semibold">{t('task.conflict')}</p>
            <p className="text-caption text-ink-muted">{t('task.conflict_hint')}</p>
          </Banner>
        ) : null}
        {general ? <Banner tone="danger">{general}</Banner> : null}

        <Card title={t('task.section_basic')}>
          <BasicFields d={d} patch={patch} />
        </Card>
        <Card title={t('task.section_schedule')}>
          <ScheduleField d={d} patch={patch} onState={(s) => setScheduleInvalid(s.invalid)} />
        </Card>
        <Card title={t('task.section_retry')}>
          <RetryFields d={d} patch={patch} />
        </Card>
        {type === 'backup' && d.backup ? (
          <Card title={t('task.section_backup')}>
            <BackupFields
              b={d.backup}
              set={(p) => setDraft((x) => (x && x.backup ? { ...x, backup: { ...x.backup, ...p } } : x))}
              sshConns={sshConns}
              repoPassword={
                creating
                  ? { mode: pwMode, setMode: setPwMode, value: repoPassword, setValue: setOwnPw, regenerate, generating }
                  : undefined
              }
            />
            {extras}
          </Card>
        ) : null}
        {type === 'sync' && d.sync ? (
          <Card title={t('task.section_sync')}>
            <SyncFields s={d.sync} set={(p) => setDraft((x) => (x && x.sync ? { ...x, sync: { ...x.sync, ...p } } : x))} conns={conns.data ?? []} />
          </Card>
        ) : null}
        <Card>
          <div className="flex flex-col gap-8">
            <HookFields which="pre" hook={d.pre} onChange={(pre) => patch({ pre })} sshConns={sshConns} />
            <HookFields which="post" hook={d.post} onChange={(post) => patch({ post })} sshConns={sshConns} />
            <p className="border-t border-hairline pt-4 text-caption text-danger">{t('task.hook_warning')}</p>
          </div>
        </Card>

        <div className="sticky bottom-0 -mx-2 flex justify-end gap-2 border-t border-hairline bg-canvas-soft/95 px-2 py-3">
          <Button onClick={() => navigate('/tasks')}>{t('common.cancel')}</Button>
          <Button variant="primary" type="submit" pending={save.isPending} disabled={scheduleInvalid}>
            {t('common.save')}
          </Button>
        </div>
      </form>

      {creating && type === 'backup' ? (
        <RecoveryDialog
          open={recoveryOpen}
          onOpenChange={setRecoveryOpen}
          task={d.name}
          repository={repoConn && d.backup ? recoveryRepository(repoConn, d.backup.repo_path) : ''}
          password={repoPassword}
          pending={save.isPending}
          onConfirm={() => send({ repo_password: repoPassword, recovery_confirmed: true })}
        />
      ) : null}

      {d.id ? (
        <Dialog
          open={deleting}
          onOpenChange={setDeleting}
          title={t('task.delete')}
          footer={
            <Button
              variant="danger"
              pending={del.isPending}
              onClick={() =>
                del.mutate(d.id!, {
                  onSuccess: () => {
                    toast(t('common.deleted'))
                    navigate('/tasks')
                  },
                })
              }
            >
              {t('common.delete')}
            </Button>
          }
        >
          <p>{t('task.delete_confirm', { name: d.name })}</p>
          {del.isError ? <Banner tone="danger">{errorText(t, del.error)}</Banner> : null}
        </Dialog>
      ) : null}
    </ErrorsProvider>
  )
}

function BasicFields({ d, patch }: { d: Draft; patch: (p: Partial<Draft>) => void }) {
  const { t } = useTranslation()
  const fe = useFieldError()
  return (
    <div className="flex flex-col gap-4">
      <Field label={t('task.name')} error={fe('name')}>
        <Input value={d.name} onChange={(e) => patch({ name: e.target.value })} autoComplete="off" />
      </Field>
      <label className="flex items-center gap-3">
        <Switch checked={d.enabled} onCheckedChange={(v) => patch({ enabled: v })} aria-label={t('task.enabled')} />
        <span>
          {t('task.enabled')}
          <span className="block text-caption text-ink-muted">{t('task.enabled_hint')}</span>
        </span>
      </label>
    </div>
  )
}

function RetryFields({ d, patch }: { d: Draft; patch: (p: Partial<Draft>) => void }) {
  const { t } = useTranslation()
  const fe = useFieldError()
  const [delays, setDelays] = useState(() => d.retry.delays_sec.map((s) => String(Number((s / 60).toFixed(2)))).join(', '))
  const [retries, setRetries] = useState(String(d.retry.retries))
  const delayError =
    fe('retry.delays_sec') ??
    d.retry.delays_sec.map((_, i) => fe(`retry.delays_sec[${i}]`)).find(Boolean) ??
    (d.retry.delays_sec.some(Number.isNaN) ? t('field.invalid') : undefined)
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field label={t('task.retries')} hint={t('task.retries_hint')} error={fe('retry.retries') ?? (Number.isNaN(d.retry.retries) ? t('field.invalid') : undefined)}>
        <Input
          inputMode="numeric"
          value={retries}
          onChange={(e) => {
            setRetries(e.target.value)
            const n = e.target.value.trim() === '' ? 0 : Number(e.target.value)
            patch({ retry: { ...d.retry, retries: Number.isInteger(n) ? n : Number.NaN } })
          }}
        />
      </Field>
      <Field label={t('task.delays')} error={delayError}>
        <Input
          value={delays}
          onChange={(e) => {
            setDelays(e.target.value)
            const parts = e.target.value
              .split(/[,，\s]+/)
              .map((x) => x.trim())
              .filter(Boolean)
            patch({ retry: { ...d.retry, delays_sec: parts.map((x) => (Number.isFinite(Number(x)) ? Math.round(Number(x) * 60) : Number.NaN)) } })
          }}
        />
      </Field>
    </div>
  )
}
