import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { ApiError } from '../../api/client'
import { errorText, fieldCodes, fieldText } from '../../api/errors'
import { useSaveConnection } from '../../api/queries'
import type { Connection } from '../../api/types'
import { Banner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { Field } from '../../ui/Field'
import { Input, SecretInput } from '../../ui/Input'

// The stored password is never sent back to the page; an empty box on edit
// means "keep it", so the request leaves the field out.
export function WebDAVConnectionForm({ conn, onSaved }: { conn?: Connection; onSaved: () => void }) {
  const { t } = useTranslation()
  const save = useSaveConnection()
  const [name, setName] = useState(conn?.name ?? '')
  const [url, setUrl] = useState(conn?.url ?? '')
  const [username, setUsername] = useState(conn?.username ?? '')
  const [password, setPassword] = useState('')

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (save.isPending) return
    save.mutate(
      {
        id: conn?.id,
        type: 'webdav',
        name,
        url,
        username,
        ...(password ? { password } : {}),
        ...(conn ? { version: conn.version } : {}),
      },
      {
        onSuccess: () => {
          setPassword('')
          onSaved()
        },
      },
    )
  }
  const fields = fieldCodes(save.error)
  const code = save.error instanceof ApiError ? save.error.code : ''
  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      <Field label={t('common.name')} error={fieldText(t, fields.name)}>
        <Input value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
      </Field>
      <Field label={t('conn.url')} error={fieldText(t, fields.url)}>
        <Input value={url} onChange={(e) => setUrl(e.target.value)} autoComplete="off" spellCheck={false} placeholder="https://" />
      </Field>
      <Field label={t('conn.username')} error={fieldText(t, fields.username)}>
        <Input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" spellCheck={false} />
      </Field>
      <Field label={t('conn.password')} error={fieldText(t, fields.password)}>
        <SecretInput
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder={conn?.has_password ? t('conn.password_keep') : undefined}
        />
      </Field>
      {save.isError && (code !== 'validation' || Object.keys(fields).length === 0) ? <Banner tone="danger">{errorText(t, save.error)}</Banner> : null}
      <div className="flex justify-end">
        <Button variant="primary" type="submit" pending={save.isPending}>
          {t('common.save')}
        </Button>
      </div>
    </form>
  )
}
