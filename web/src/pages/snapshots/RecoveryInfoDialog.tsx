import { Download } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { ApiError } from '../../api/client'
import { errorText } from '../../api/errors'
import { useRecoveryInfo } from '../../api/queries'
import type { Recovery } from '../../api/types'
import { downloadText } from '../../lib/download'
import { recoveryText } from '../../lib/recovery'
import { Banner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { CodeBlock } from '../../ui/CodeBlock'
import { CopyButton } from '../../ui/CopyButton'
import { Dialog } from '../../ui/Dialog'
import { Field } from '../../ui/Field'
import { SecretInput } from '../../ui/Input'

// RecoveryInfoDialog shows a backup task's repository password again, after
// the admin password. Nothing of it outlives the dialog.
export function RecoveryInfoDialog({ taskId, taskName, open, onOpenChange }: { taskId: string; taskName: string; open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation()
  const ask = useRecoveryInfo()
  const [password, setPassword] = useState('')
  const [info, setInfo] = useState<Recovery | null>(null)
  const [error, setError] = useState<string>()

  const close = (o: boolean) => {
    if (!o) {
      setPassword('')
      setInfo(null)
      setError(undefined)
      ask.reset()
    }
    onOpenChange(o)
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (ask.isPending || !password) return
    const pw = password
    setPassword('')
    ask.mutate(
      { id: taskId, password: pw },
      {
        onSuccess: (r) => {
          setError(undefined)
          setInfo(r)
          ask.reset() // the answer lives in this component only, not in the mutation cache
        },
        onError: (err) => setError(err instanceof ApiError && err.status === 403 ? t('rec.wrong') : errorText(t, err)),
      },
    )
  }

  return (
    <Dialog open={open} onOpenChange={close} wide title={t('rec.title')} description={info ? undefined : t('rec.desc')}>
      {info ? (
        <>
          <div className="flex flex-col gap-1">
            <p className="text-eyebrow text-ink-secondary">{t('task.recovery_repo')}</p>
            <CodeBlock text={info.repository} copyable />
          </div>
          <div className="flex flex-col gap-1">
            <p className="text-eyebrow text-ink-secondary">{t('task.recovery_password')}</p>
            <CodeBlock text={info.password} copyable />
          </div>
          <div className="flex flex-col gap-1">
            <p className="text-eyebrow text-ink-secondary">{t('rec.commands')}</p>
            {info.commands.map((c) => (
              <CodeBlock key={c} text={c} />
            ))}
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              icon={<Download aria-hidden className="size-4" />}
              onClick={() => downloadText(`tern-recovery-${taskName}.txt`, recoveryText({ task: taskName, repository: info.repository, password: info.password }))}
            >
              {t('task.recovery_download')}
            </Button>
            <CopyButton text={recoveryText({ task: taskName, repository: info.repository, password: info.password })} className="px-[14px] py-1 text-body" />
          </div>
        </>
      ) : (
        <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
          <Field label={t('rec.password')} error={error}>
            <SecretInput value={password} onChange={(e) => setPassword(e.target.value)} autoFocus />
          </Field>
          {ask.error instanceof ApiError && ask.error.status === 429 ? <Banner tone="danger">{errorText(t, ask.error)}</Banner> : null}
          <div className="flex justify-end">
            <Button variant="primary" type="submit" pending={ask.isPending}>
              {t('rec.show')}
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  )
}
