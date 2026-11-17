import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { ApiError, request } from '../../api/client'
import { errorText } from '../../api/errors'
import { useStatus } from '../../api/queries'
import { markStatus } from '../../lib/session'
import { AuthCard } from '../../shell/AuthCard'
import { Button } from '../../ui/Button'
import { Field } from '../../ui/Field'
import { SecretInput } from '../../ui/Input'

// UnlockPage only ever shows for a node that an earlier tern set up with a
// separate unlock passphrase; the server moves it to unlocking by itself
// once the passphrase has been entered.
export function UnlockPage() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const secretsMounted = useStatus().data?.mounts?.secrets !== false
  const [passphrase, setPassphrase] = useState('')
  const [error, setError] = useState<string>()

  const unlock = useMutation({
    mutationFn: (p: string) => request<void>('POST', '/api/v1/unlock', { passphrase: p }),
    onSuccess: () => {
      setError(undefined)
      markStatus(qc, { vault_state: 'unlocked' })
    },
    onError: (e) => {
      unlock.reset() // the attempt held the passphrase
      setError(e instanceof ApiError && e.status === 403 ? t('unlock.wrong') : errorText(t, e))
    },
  })

  const submit = (ev: FormEvent) => {
    ev.preventDefault()
    if (unlock.isPending || passphrase === '') return
    const p = passphrase
    setPassphrase('')
    unlock.mutate(p)
  }

  return (
    <AuthCard title={t('unlock.title')}>
      <p className="mb-4 text-caption text-ink-muted">{t('unlock.desc')}</p>
      {secretsMounted ? null : <p className="mb-4 text-caption text-danger">{t('unlock.no_secrets')}</p>}
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        <Field label={t('unlock.passphrase')} error={error}>
          <SecretInput value={passphrase} onChange={(e) => setPassphrase(e.target.value)} autoFocus />
        </Field>
        <Button variant="primary" type="submit" pending={unlock.isPending} className="self-end">
          {t('unlock.submit')}
        </Button>
      </form>
    </AuthCard>
  )
}
