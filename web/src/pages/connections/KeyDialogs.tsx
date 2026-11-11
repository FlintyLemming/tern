import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { ApiError } from '../../api/client'
import { errorText, fieldCodes, fieldText } from '../../api/errors'
import { useCreateKey, useDeleteKey, useRenameKey } from '../../api/queries'
import type { SSHKey } from '../../api/types'
import { Banner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { CodeBlock } from '../../ui/CodeBlock'
import { Dialog } from '../../ui/Dialog'
import { Field } from '../../ui/Field'
import { Input, SecretInput } from '../../ui/Input'
import { Textarea } from '../../ui/Textarea'
import { useToast } from '../../ui/Toast'

interface Props {
  open: boolean
  onOpenChange: (o: boolean) => void
}

// PublicKeyResult is what a new key looks like: its public half, ready to copy.
function PublicKeyResult({ k, onDone }: { k: SSHKey; onDone: () => void }) {
  const { t } = useTranslation()
  return (
    <>
      <p className="text-body font-semibold">{t('keys.add_to_authorized')}</p>
      <CodeBlock text={k.public_key} copyable />
      <p className="text-caption text-ink-muted">
        {t('keys.col_fingerprint')}: <span className="font-mono">{k.fingerprint}</span>
      </p>
      <div className="flex justify-end">
        <Button variant="primary" onClick={onDone}>
          {t('common.done')}
        </Button>
      </div>
    </>
  )
}

export function GenerateKeyDialog({ open, onOpenChange }: Props) {
  const { t } = useTranslation()
  const create = useCreateKey()
  const [name, setName] = useState('')
  const [made, setMade] = useState<SSHKey>()
  const close = (o: boolean) => {
    if (!o) {
      setName('')
      setMade(undefined)
      create.reset()
    }
    onOpenChange(o)
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (create.isPending) return
    create.mutate({ name, mode: 'generate' }, { onSuccess: setMade })
  }
  const fields = fieldCodes(create.error)
  return (
    <Dialog open={open} onOpenChange={close} title={t('keys.generate_title')}>
      {made ? (
        <PublicKeyResult k={made} onDone={() => close(false)} />
      ) : (
        <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
          <p className="text-caption text-ink-muted">{t('keys.generate_desc')}</p>
          <Field label={t('common.name')} error={fieldText(t, fields.name)}>
            <Input value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
          </Field>
          {create.isError && !fields.name ? <Banner tone="danger">{errorText(t, create.error)}</Banner> : null}
          <div className="flex justify-end">
            <Button variant="primary" type="submit" pending={create.isPending}>
              {t('keys.generate_submit')}
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  )
}

export function ImportKeyDialog({ open, onOpenChange }: Props) {
  const { t } = useTranslation()
  const create = useCreateKey()
  const [name, setName] = useState('')
  const [priv, setPriv] = useState('')
  const [pass, setPass] = useState('')
  const [made, setMade] = useState<SSHKey>()
  const close = (o: boolean) => {
    if (!o) {
      setName('')
      setPriv('')
      setPass('')
      setMade(undefined)
      create.reset()
    }
    onOpenChange(o)
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (create.isPending) return
    create.mutate(
      { name, mode: 'import', private_key: priv, ...(pass ? { passphrase: pass } : {}) },
      {
        onSuccess: (k) => {
          // the private key and its passphrase leave the page the moment tern has them
          setPriv('')
          setPass('')
          setMade(k)
          create.reset() // the request (with the key in it) is dropped as well
        },
      },
    )
  }
  const fields = fieldCodes(create.error)
  const code = create.error instanceof ApiError ? create.error.code : ''
  return (
    <Dialog open={open} onOpenChange={close} title={t('keys.import_title')} wide>
      {made ? (
        <PublicKeyResult k={made} onDone={() => close(false)} />
      ) : (
        <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
          <Field label={t('common.name')} error={fieldText(t, fields.name)}>
            <Input value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
          </Field>
          <Field
            label={t('keys.private_key')}
            hint={t('keys.private_key_hint')}
            error={code === 'unsupported_key' ? errorText(t, create.error) : fieldText(t, fields.private_key)}
          >
            <Textarea mono rows={8} value={priv} onChange={(e) => setPriv(e.target.value)} autoComplete="off" />
          </Field>
          <Field
            label={t('keys.passphrase')}
            error={code === 'passphrase_required' || code === 'bad_passphrase' ? errorText(t, create.error) : fieldText(t, fields.passphrase)}
          >
            <SecretInput value={pass} onChange={(e) => setPass(e.target.value)} />
          </Field>
          {create.isError && !['unsupported_key', 'passphrase_required', 'bad_passphrase', 'validation'].includes(code) ? (
            <Banner tone="danger">{errorText(t, create.error)}</Banner>
          ) : null}
          <div className="flex justify-end">
            <Button variant="primary" type="submit" pending={create.isPending}>
              {t('keys.import_submit')}
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  )
}

export function RenameKeyDialog({ k, onClose }: { k: SSHKey | null; onClose: () => void }) {
  const { t } = useTranslation()
  const toast = useToast()
  const rename = useRenameKey()
  const [name, setName] = useState('')
  const [shown, setShown] = useState<string>()
  if (k && shown !== k.id) {
    setShown(k.id)
    setName(k.name)
    rename.reset()
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!k || rename.isPending) return
    rename.mutate(
      { id: k.id, name, version: k.version },
      {
        onSuccess: () => {
          toast(t('common.saved'))
          setShown(undefined)
          onClose()
        },
      },
    )
  }
  const fields = fieldCodes(rename.error)
  return (
    <Dialog
      open={Boolean(k)}
      onOpenChange={(o) => {
        if (!o) {
          setShown(undefined)
          onClose()
        }
      }}
      title={t('keys.rename_title')}
    >
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        <Field label={t('common.name')} error={fieldText(t, fields.name)}>
          <Input value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
        </Field>
        {rename.isError && !fields.name ? <Banner tone="danger">{errorText(t, rename.error)}</Banner> : null}
        <div className="flex justify-end">
          <Button variant="primary" type="submit" pending={rename.isPending}>
            {t('common.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

export function DeleteKeyDialog({ k, onClose }: { k: SSHKey | null; onClose: () => void }) {
  const { t } = useTranslation()
  const toast = useToast()
  const del = useDeleteKey()
  const inUse = del.error instanceof ApiError && del.error.code === 'in_use'
  return (
    <Dialog
      open={Boolean(k)}
      onOpenChange={(o) => {
        if (!o) {
          del.reset()
          onClose()
        }
      }}
      title={t('keys.delete_title')}
      footer={
        <Button
          variant="danger"
          pending={del.isPending}
          onClick={() =>
            k &&
            del.mutate(k.id, {
              onSuccess: () => {
                toast(t('common.deleted'))
                onClose()
              },
            })
          }
        >
          {t('common.delete')}
        </Button>
      }
    >
      <p>{t('keys.delete_confirm', { name: k?.name })}</p>
      {del.isError ? <Banner tone="danger">{inUse ? t('keys.in_use') : errorText(t, del.error)}</Banner> : null}
    </Dialog>
  )
}
