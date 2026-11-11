import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { ApiError } from '../../api/client'
import { errorText, fieldCodes, fieldText } from '../../api/errors'
import { useKeys, useProbe, useSaveConnection } from '../../api/queries'
import type { Connection, ProbeResult } from '../../api/types'
import { Banner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { Checkbox } from '../../ui/Checkbox'
import { Field } from '../../ui/Field'
import { Input } from '../../ui/Input'
import { Select } from '../../ui/Select'

// The host key algorithm as the file name on the remote spells it.
const hostKeyFile = (type: string) => type.replace(/^ssh-/, '').replace(/^ecdsa-sha2-nistp\d+$/, 'ecdsa')

// SSHConnectionForm saves only a host key a person has looked at: fetch the
// fingerprint, compare it on the remote, tick the box. Moving the connection
// to another host or port throws the confirmation away.
export function SSHConnectionForm({ conn, onSaved }: { conn?: Connection; onSaved: () => void }) {
  const { t } = useTranslation()
  const keys = useKeys()
  const probe = useProbe()
  const save = useSaveConnection()
  const [name, setName] = useState(conn?.name ?? '')
  const [host, setHost] = useState(conn?.host ?? '')
  const [port, setPort] = useState(String(conn?.port ?? 22))
  const [username, setUsername] = useState(conn?.username ?? '')
  const [keyId, setKeyId] = useState(conn?.ssh_key_id ?? '')
  const [found, setFound] = useState<ProbeResult | null>(
    conn ? { type: conn.host_key.split(' ')[0], authorized: conn.host_key, fingerprint: conn.host_key_fingerprint } : null,
  )
  const [confirmed, setConfirmed] = useState(Boolean(conn))

  const moved = () => {
    setFound(null)
    setConfirmed(false)
    probe.reset()
  }
  const portNum = Number(port)

  const doProbe = () => {
    if (probe.isPending) return
    moved()
    probe.mutate({ host, port: Number.isInteger(portNum) && portNum > 0 ? portNum : 0 }, { onSuccess: setFound })
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!found || !confirmed || save.isPending) return
    save.mutate(
      {
        id: conn?.id,
        type: 'ssh',
        name,
        host,
        port: portNum,
        username,
        ssh_key_id: keyId,
        host_key: found.authorized,
        ...(conn ? { version: conn.version } : {}),
      },
      { onSuccess: onSaved },
    )
  }

  const fields = { ...fieldCodes(probe.error), ...fieldCodes(save.error) }
  const probeFailed = probe.error instanceof ApiError && probe.error.code === 'unreachable'
  const saveCode = save.error instanceof ApiError ? save.error.code : ''

  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      <Field label={t('common.name')} error={fieldText(t, fields.name)}>
        <Input value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
      </Field>
      <div className="grid grid-cols-[1fr_7rem] gap-3">
        <Field label={t('conn.host')} error={fieldText(t, fields.host)}>
          <Input
            value={host}
            onChange={(e) => {
              setHost(e.target.value)
              moved()
            }}
            autoComplete="off"
            spellCheck={false}
          />
        </Field>
        <Field label={t('conn.port')} error={fieldText(t, fields.port)}>
          <Input
            value={port}
            inputMode="numeric"
            onChange={(e) => {
              setPort(e.target.value)
              moved()
            }}
          />
        </Field>
      </div>
      <Field label={t('conn.username')} error={fieldText(t, fields.username)}>
        <Input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" spellCheck={false} />
      </Field>
      <Field label={t('conn.key')} error={fieldText(t, fields.ssh_key_id)} hint={keys.data?.length === 0 ? t('conn.no_keys') : undefined}>
        <Select
          value={keyId}
          onValueChange={setKeyId}
          placeholder={t('conn.key_placeholder')}
          options={(keys.data ?? []).map((k) => ({ value: k.id, label: k.name }))}
        />
      </Field>

      <div className="flex flex-col gap-3 rounded-lg border border-hairline bg-canvas-soft p-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="text-caption text-ink-muted">{t('conn.probe_hint')}</p>
          <Button onClick={doProbe} pending={probe.isPending} disabled={!host}>
            {t('conn.probe')}
          </Button>
        </div>
        {probe.isError ? (
          <Banner tone="danger">{probeFailed ? t('conn.test_result.network') : errorText(t, probe.error)}</Banner>
        ) : null}
        {found ? (
          <div className="flex flex-col gap-3 rounded-md border-2 border-ink-secondary bg-surface p-4">
            {conn && confirmed && found.authorized === conn.host_key ? (
              <p className="text-eyebrow text-ink-muted">{t('conn.confirmed_before')}</p>
            ) : null}
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
              <dt className="text-ink-muted">{t('conn.fingerprint_type')}</dt>
              <dd className="font-mono">{found.type}</dd>
              <dt className="text-ink-muted">{t('conn.fingerprint')}</dt>
              <dd className="font-mono break-all">{found.fingerprint}</dd>
            </dl>
            <p className="text-caption text-ink-muted">{t('conn.probe_check', { alg: hostKeyFile(found.type) })}</p>
            <Checkbox checked={confirmed} onChange={setConfirmed}>
              {t('conn.confirm_fingerprint')}
            </Checkbox>
          </div>
        ) : null}
      </div>

      {save.isError && (saveCode !== 'validation' || Object.keys(fields).length === 0) ? <Banner tone="danger">{errorText(t, save.error)}</Banner> : null}
      <div className="flex justify-end">
        <Button variant="primary" type="submit" pending={save.isPending} disabled={!found || !confirmed}>
          {t('common.save')}
        </Button>
      </div>
    </form>
  )
}
