import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { request, setCsrf } from '../../api/client'
import { useStatus } from '../../api/queries'
import { errorText, fieldCodes, fieldText } from '../../api/errors'
import type { CsrfToken, UnlockMode } from '../../api/types'
import { cx } from '../../lib/cx'
import { markStatus } from '../../lib/session'
import { NODE_NAME_RE, secretError } from '../../lib/validate'
import { AuthCard } from '../../shell/AuthCard'
import { Banner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { Field } from '../../ui/Field'
import { Input, SecretInput } from '../../ui/Input'

type Errors = Record<string, string | undefined>

const STEP_OF: Record<string, number> = { password: 1, node_name: 1, unlock_mode: 2, passphrase: 2 }

export function SetupPage() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [step, setStep] = useState(1)
  const [password, setPassword] = useState('')
  const [again, setAgain] = useState('')
  const [nodeName, setNodeName] = useState('')
  const [chosenMode, setMode] = useState<UnlockMode>('keyfile')
  const [passphrase, setPassphrase] = useState('')
  const [passAgain, setPassAgain] = useState('')
  const [errors, setErrors] = useState<Errors>({})
  // Without the volumes the server refuses setup: say so before the form.
  const mounts = useStatus().data?.mounts
  const keyfileOK = mounts?.secrets !== false
  const mode: UnlockMode = keyfileOK ? chosenMode : 'passphrase'

  const clearSecrets = () => {
    setPassword('')
    setAgain('')
    setPassphrase('')
    setPassAgain('')
  }

  const setup = useMutation({
    mutationFn: () =>
      request<CsrfToken>('POST', '/api/v1/setup', {
        password,
        node_name: nodeName,
        unlock_mode: mode,
        ...(mode === 'passphrase' ? { passphrase } : {}),
      }),
    onSuccess: (r) => {
      setCsrf(r.csrf_token)
      clearSecrets()
      markStatus(qc, { setup_required: false, authenticated: true, vault_state: 'unlocked' }) // the gate moves on to the overview
    },
    onError: (e) => {
      const fields = fieldCodes(e)
      const errs: Errors = {}
      for (const [k, code] of Object.entries(fields)) errs[k] = fieldText(t, code)
      setErrors(errs)
      const first = Math.min(...Object.keys(fields).map((k) => STEP_OF[k] ?? 3))
      if (Number.isFinite(first)) setStep(first)
    },
  })

  const checkStep1 = (): Errors => {
    const e: Errors = {}
    const pw = secretError(password)
    if (pw) e.password = t(pw)
    else if (again !== password) e.again = t('v.mismatch')
    if (!NODE_NAME_RE.test(nodeName)) e.node_name = t(nodeName ? 'v.node_name' : 'v.required')
    return e
  }
  const checkStep2 = (): Errors => {
    const e: Errors = {}
    if (mode !== 'passphrase') return e
    const pp = secretError(passphrase)
    if (pp) e.passphrase = t(pp)
    else if (passAgain !== passphrase) e.pass_again = t('v.mismatch')
    return e
  }

  const next = (ev: FormEvent) => {
    ev.preventDefault()
    if (step === 3) {
      if (!setup.isPending) setup.mutate()
      return
    }
    const e = step === 1 ? checkStep1() : checkStep2()
    setErrors(e)
    if (Object.keys(e).length === 0) setStep(step + 1)
  }

  const general = setup.isError && Object.keys(fieldCodes(setup.error)).length === 0 ? errorText(t, setup.error) : undefined

  if (mounts?.data === false) {
    return (
      <AuthCard title={t('setup.title')}>
        <Banner tone="danger">
          <p className="font-semibold">{t('setup.no_data_title')}</p>
          <p className="mt-1">{t('setup.no_data')}</p>
        </Banner>
      </AuthCard>
    )
  }

  return (
    <AuthCard title={t('setup.title')}>
      <p className="mb-1 text-eyebrow text-ink-muted">{t('setup.step', { n: step })}</p>
      <h2 className="mb-4 text-title">{t(`setup.step${step}`)}</h2>
      <form onSubmit={next} className="flex flex-col gap-4" noValidate>
        {step === 1 ? (
          <>
            <Field label={t('setup.password')} hint={t('setup.password_hint')} error={errors.password}>
              <SecretInput value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
            <Field label={t('setup.password_again')} error={errors.again}>
              <SecretInput value={again} onChange={(e) => setAgain(e.target.value)} />
            </Field>
            <Field label={t('setup.node_name')} hint={t('setup.node_name_hint')} error={errors.node_name}>
              <Input value={nodeName} onChange={(e) => setNodeName(e.target.value)} autoComplete="off" spellCheck={false} />
            </Field>
          </>
        ) : null}

        {step === 2 ? (
          <>
            <fieldset className="flex flex-col gap-3">
              <legend className="sr-only">{t('setup.step2')}</legend>
              {(['keyfile', 'passphrase'] as const).map((m) => {
                const off = m === 'keyfile' && !keyfileOK
                return (
                  <label
                    key={m}
                    className={cx(
                      'flex gap-3 rounded-md border p-4',
                      off ? 'cursor-not-allowed opacity-60' : 'cursor-pointer',
                      mode === m ? 'border-primary' : 'border-hairline',
                    )}
                  >
                    <input
                      type="radio"
                      name="unlock_mode"
                      value={m}
                      checked={mode === m}
                      disabled={off}
                      onChange={() => setMode(m)}
                      className="mt-1 accent-[var(--color-primary)]"
                    />
                    <span className="flex flex-col gap-1">
                      <span className="font-semibold">{t(`setup.mode_${m}`)}</span>
                      <span className="text-caption text-ink-muted">{t(`setup.mode_${m}_desc`)}</span>
                      {off ? <span className="text-caption text-danger">{t('setup.no_secrets')}</span> : null}
                    </span>
                  </label>
                )
              })}
            </fieldset>
            {errors.unlock_mode ? <p className="text-caption text-danger">{errors.unlock_mode}</p> : null}
            {mode === 'passphrase' ? (
              <>
                <Field label={t('setup.passphrase')} error={errors.passphrase}>
                  <SecretInput value={passphrase} onChange={(e) => setPassphrase(e.target.value)} />
                </Field>
                <Field label={t('setup.passphrase_again')} error={errors.pass_again}>
                  <SecretInput value={passAgain} onChange={(e) => setPassAgain(e.target.value)} />
                </Field>
              </>
            ) : null}
          </>
        ) : null}

        {step === 3 ? (
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-body">
            <dt className="text-ink-muted">{t('setup.summary_node')}</dt>
            <dd className="font-semibold">{nodeName}</dd>
            <dt className="text-ink-muted">{t('setup.summary_mode')}</dt>
            <dd className="font-semibold">{t(`setup.mode_${mode}`)}</dd>
            <dd className="col-span-2 mt-2 text-caption text-ink-muted">{t('setup.summary_note')}</dd>
          </dl>
        ) : null}

        {general ? <Banner tone="danger">{general}</Banner> : null}

        <div className="mt-2 flex items-center justify-between gap-2">
          {step > 1 ? (
            <Button variant="utility" onClick={() => setStep(step - 1)} disabled={setup.isPending}>
              {t('setup.back')}
            </Button>
          ) : (
            <span />
          )}
          <Button variant="primary" type="submit" pending={setup.isPending}>
            {step === 3 ? t('setup.submit') : t('setup.next')}
          </Button>
        </div>
      </form>
    </AuthCard>
  )
}
