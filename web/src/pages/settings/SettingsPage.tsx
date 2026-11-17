import { useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { ApiError } from '../../api/client'
import { errorText, fieldCodes, fieldText } from '../../api/errors'
import { forgetSession, useChangePassword, useSaveSettings, useSettings, useSystem } from '../../api/queries'
import type { Settings } from '../../api/types'
import { setLang, type Lang } from '../../i18n'
import { zoneLabel } from '../../lib/format'
import { NODE_NAME_RE, secretError } from '../../lib/validate'
import { Banner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { Card } from '../../ui/Card'
import { Field } from '../../ui/Field'
import { Input, SecretInput } from '../../ui/Input'
import { PageHeader } from '../../ui/PageHeader'
import { Table } from '../../ui/Table'
import { useToast } from '../../ui/Toast'

type Errs = Record<string, string | undefined>

function NodeAndRuns({ s }: { s: Settings }) {
  const { t } = useTranslation()
  const toast = useToast()
  const sys = useSystem()
  const save = useSaveSettings()
  const [nodeName, setNodeName] = useState(s.node_name)
  const [conc, setConc] = useState(String(s.max_concurrent))
  const [days, setDays] = useState(String(s.log_keep_days))
  const [runs, setRuns] = useState(String(s.log_keep_runs))
  const [errs, setErrs] = useState<Errs>({})

  const inRange = (v: string, lo: number, hi: number) => /^\d+$/.test(v.trim()) && Number(v) >= lo && Number(v) <= hi

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (save.isPending) return
    const local: Errs = {}
    if (!NODE_NAME_RE.test(nodeName)) local.node_name = t('v.node_name')
    if (!inRange(conc, 1, 8)) local.max_concurrent = t('settings.range_concurrent')
    if (!inRange(days, 1, 3650)) local.log_keep_days = t('settings.range_days')
    if (!inRange(runs, 1, 10000)) local.log_keep_runs = t('settings.range_runs')
    setErrs(local)
    if (Object.keys(local).length) return
    save.mutate(
      { node_name: nodeName, max_concurrent: Number(conc), log_keep_days: Number(days), log_keep_runs: Number(runs) },
      { onSuccess: () => toast(t('common.saved')) },
    )
  }
  const server = fieldCodes(save.error)
  const err = (k: string) => errs[k] ?? fieldText(t, server[k])

  return (
    <Card title={t('settings.node')}>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={t('settings.node_name')} hint={t('settings.node_name_hint')} error={err('node_name')}>
            <Input value={nodeName} onChange={(e) => setNodeName(e.target.value)} autoComplete="off" spellCheck={false} />
          </Field>
          <div className="flex flex-col gap-1">
            <p className="text-eyebrow text-ink-secondary">{t('settings.timezone')}</p>
            <p className="py-[7px] font-mono">{sys.data?.timezone ?? '…'}</p>
            <p className="text-caption text-ink-muted">
              {sys.data && zoneLabel(sys.data.timezone) !== sys.data.timezone ? t('settings.timezone_unknown') : t('settings.timezone_hint')}
            </p>
          </div>
          <div className="flex flex-col gap-1">
            <p className="text-eyebrow text-ink-secondary">{t('settings.version')}</p>
            <p className="py-[7px] font-mono">{sys.data?.version ?? '…'}</p>
          </div>
        </div>
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={t('settings.max_concurrent')} hint={t('settings.max_concurrent_hint')} error={err('max_concurrent')}>
            <Input inputMode="numeric" value={conc} onChange={(e) => setConc(e.target.value)} />
          </Field>
          <Field label={t('settings.log_keep_days')} error={err('log_keep_days')}>
            <Input inputMode="numeric" value={days} onChange={(e) => setDays(e.target.value)} />
          </Field>
          <Field label={t('settings.log_keep_runs')} error={err('log_keep_runs')}>
            <Input inputMode="numeric" value={runs} onChange={(e) => setRuns(e.target.value)} />
          </Field>
        </div>
        {save.isError && Object.keys(server).length === 0 ? <Banner tone="danger">{errorText(t, save.error)}</Banner> : null}
        <div className="flex justify-end">
          <Button variant="primary" type="submit" pending={save.isPending}>
            {t('settings.save')}
          </Button>
        </div>
      </form>
    </Card>
  )
}

function ChangePassword() {
  const { t } = useTranslation()
  const toast = useToast()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const change = useChangePassword()
  const [cur, setCur] = useState('')
  const [next, setNext] = useState('')
  const [again, setAgain] = useState('')
  const [errs, setErrs] = useState<Errs>({})
  const [other, setOther] = useState<string>()

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (change.isPending) return
    setOther(undefined)
    const local: Errs = {}
    if (!cur) local.cur = t('v.required')
    const ne = secretError(next)
    if (ne) local.next = t(ne)
    else if (again !== next) local.again = t('v.mismatch')
    setErrs(local)
    if (Object.keys(local).length) return
    change.mutate(
      { current_password: cur, new_password: next },
      {
        onSuccess: () => {
          setCur('')
          setNext('')
          setAgain('')
          toast(t('settings.password_changed'))
          // every session ended with the old password, this one included
          forgetSession(qc)
          navigate('/login')
        },
        onError: (er) => {
          change.reset() // the attempt held both passwords
          setCur('')
          if (er instanceof ApiError && er.status === 403) setErrs({ cur: t('error.forbidden') })
          else if (fieldCodes(er).new_password) setErrs({ next: fieldText(t, fieldCodes(er).new_password) })
          else setOther(errorText(t, er))
        },
      },
    )
  }
  return (
    <Card title={t('settings.password_title')}>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        <Field label={t('settings.current_password')} error={errs.cur}>
          <SecretInput value={cur} onChange={(e) => setCur(e.target.value)} />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t('settings.new_password')} hint={t('setup.password_hint')} error={errs.next}>
            <SecretInput value={next} onChange={(e) => setNext(e.target.value)} />
          </Field>
          <Field label={t('settings.new_password_again')} error={errs.again}>
            <SecretInput value={again} onChange={(e) => setAgain(e.target.value)} />
          </Field>
        </div>
        {other ? <Banner tone="danger">{other}</Banner> : null}
        <div className="flex justify-end">
          <Button type="submit" pending={change.isPending}>
            {t('settings.change_password')}
          </Button>
        </div>
      </form>
    </Card>
  )
}

function Tools() {
  const { t } = useTranslation()
  const sys = useSystem()
  return (
    <Card title={t('settings.tools_title')}>
      <Table
        rows={sys.data?.tools ?? []}
        rowKey={(x) => x.name}
        empty={t('common.loading')}
        columns={[
          { key: 'name', header: t('settings.tool'), render: (x) => <span className="font-mono">{x.name}</span> },
          { key: 'version', header: t('settings.tool_version'), render: (x) => <span className="font-mono">{x.version || '—'}</span>, title: (x) => x.version },
          {
            key: 'found',
            header: t('settings.tool_state'),
            render: (x) =>
              x.found ? <span className="text-ink-muted">{t('settings.tool_found')}</span> : <span className="font-semibold text-danger">{t('settings.tool_missing')}</span>,
          },
        ]}
      />
    </Card>
  )
}

function Language() {
  const { t, i18n } = useTranslation()
  const langs: [Lang, string][] = [
    ['zh', '中文'],
    ['en', 'English'],
  ]
  return (
    <Card title={t('settings.language_title')}>
      <div role="radiogroup" aria-label={t('settings.language_title')} className="flex gap-6">
        {langs.map(([code, label]) => (
          <label key={code} className="inline-flex items-center gap-2">
            <input type="radio" name="lang" checked={i18n.language === code} onChange={() => setLang(code)} className="accent-[var(--color-primary)]" />
            {label}
          </label>
        ))}
      </div>
    </Card>
  )
}

export function SettingsPage() {
  const { t } = useTranslation()
  const settings = useSettings()
  return (
    <>
      <PageHeader title={t('settings.title')} />
      <div className="flex flex-col gap-6">
        {settings.isError ? <Banner tone="danger">{errorText(t, settings.error)}</Banner> : null}
        {settings.data ? <NodeAndRuns s={settings.data} /> : <p className="text-caption text-ink-muted">{t('common.loading')}</p>}
        <ChangePassword />
        <Tools />
        <Language />
      </div>
    </>
  )
}
