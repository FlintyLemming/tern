import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { ApiError, request, setCsrf } from '../../api/client'
import { errorText } from '../../api/errors'
import type { CsrfToken } from '../../api/types'
import { markStatus } from '../../lib/session'
import { AuthCard } from '../../shell/AuthCard'
import { Banner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { Field } from '../../ui/Field'
import { SecretInput } from '../../ui/Input'

// LoginPage: one password field. Where to go afterwards (?next=, same site
// only) is the gate's job once the status says we are in.
export function LoginPage() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [password, setPassword] = useState('')
  const [until, setUntil] = useState(0) // rate limited until this instant (ms)
  const [now, setNow] = useState(() => Date.now())
  const [error, setError] = useState<string>()
  const wait = Math.max(0, Math.ceil((until - now) / 1000))

  useEffect(() => {
    if (until <= Date.now()) return
    // a deadline, not a chain of one-second timeouts: a throttled background
    // tab still shows the right number when it comes back
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [until])

  const login = useMutation({
    mutationFn: (pw: string) => request<CsrfToken>('POST', '/api/v1/login', { password: pw }),
    onSuccess: (r) => {
      setCsrf(r.csrf_token)
      setError(undefined)
      markStatus(qc, { authenticated: true })
    },
    onError: (e) => {
      if (e instanceof ApiError && e.status === 429) {
        setNow(Date.now())
        setUntil(Date.now() + (e.retryAfter ?? 60) * 1000)
        setError(undefined)
      } else if (e instanceof ApiError && e.status === 401) {
        setError(t('login.wrong'))
      } else {
        setError(errorText(t, e))
      }
    },
  })

  const submit = (ev: FormEvent) => {
    ev.preventDefault()
    if (login.isPending || wait > 0 || password === '') return
    const pw = password
    setPassword('') // the secret leaves the component state as soon as it is sent
    login.mutate(pw)
  }

  return (
    <AuthCard title={t('login.title')}>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        <Field label={t('login.password')} error={error}>
          <SecretInput value={password} onChange={(e) => setPassword(e.target.value)} autoFocus />
        </Field>
        {wait > 0 ? <Banner tone="danger">{t('login.wait', { n: wait })}</Banner> : null}
        <Button variant="primary" type="submit" pending={login.isPending} disabled={wait > 0} className="self-end">
          {t('login.submit')}
        </Button>
      </form>
    </AuthCard>
  )
}
