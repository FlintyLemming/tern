import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router'
import { ApiError } from '../../api/client'
import { errorText } from '../../api/errors'
import { useConnections, useDeleteConnection, useKeys, useTestConnection } from '../../api/queries'
import type { Connection, SSHKey, TestResult } from '../../api/types'
import { NodeTime } from '../../shell/NodeTime'
import { Banner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { CopyButton } from '../../ui/CopyButton'
import { Dialog } from '../../ui/Dialog'
import { PageHeader } from '../../ui/PageHeader'
import { StatusDot } from '../../ui/StatusDot'
import { Table } from '../../ui/Table'
import { Tabs } from '../../ui/Tabs'
import { useToast } from '../../ui/Toast'
import { DeleteKeyDialog, GenerateKeyDialog, ImportKeyDialog, RenameKeyDialog } from './KeyDialogs'
import { SSHConnectionForm } from './SSHConnectionForm'
import { WebDAVConnectionForm } from './WebDAVConnectionForm'

function KeysTab() {
  const { t } = useTranslation()
  const keys = useKeys()
  const [gen, setGen] = useState(false)
  const [imp, setImp] = useState(false)
  const [renaming, setRenaming] = useState<SSHKey | null>(null)
  const [deleting, setDeleting] = useState<SSHKey | null>(null)
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap justify-end gap-2">
        <Button variant="utility" onClick={() => setImp(true)}>
          {t('keys.import')}
        </Button>
        <Button variant="primary" onClick={() => setGen(true)}>
          {t('keys.generate')}
        </Button>
      </div>
      {keys.isError ? <Banner tone="danger">{errorText(t, keys.error)}</Banner> : null}
      <Table
        rows={keys.data ?? []}
        rowKey={(k) => k.id}
        empty={keys.isPending ? t('common.loading') : t('keys.empty')}
        columns={[
          { key: 'name', header: t('common.name') },
          { key: 'fingerprint', header: t('keys.col_fingerprint'), render: (k) => <span className="font-mono text-caption">{k.fingerprint}</span>, title: (k) => k.fingerprint },
          { key: 'pass', header: t('keys.col_passphrase'), render: (k) => (k.has_passphrase ? t('common.yes') : t('common.no')) },
          { key: 'created', header: t('keys.col_created'), render: (k) => <NodeTime iso={k.created_at} /> },
          {
            key: 'actions',
            header: t('common.actions'),
            render: (k) => (
              <div className="flex gap-2">
                <CopyButton text={k.public_key} label={t('keys.copy_public')} />
                <Button onClick={() => setRenaming(k)}>{t('common.rename')}</Button>
                <Button onClick={() => setDeleting(k)}>{t('common.delete')}</Button>
              </div>
            ),
          },
        ]}
      />
      <GenerateKeyDialog open={gen} onOpenChange={setGen} />
      <ImportKeyDialog open={imp} onOpenChange={setImp} />
      <RenameKeyDialog k={renaming} onClose={() => setRenaming(null)} />
      <DeleteKeyDialog k={deleting} onClose={() => setDeleting(null)} />
    </div>
  )
}

function testMessage(t: (k: string) => string, r: TestResult): string {
  if (r.ok) return t('conn.test_result.ok')
  if (r.kind === 'network' || r.kind === 'auth' || r.kind === 'host_key') return t(`conn.test_result.${r.kind}`)
  return r.message // config and the rest: the server says what is wrong
}

function address(c: Connection): string {
  if (c.type === 'webdav') return c.url
  const host = c.host.includes(':') ? `[${c.host}]` : c.host
  return `${c.username}@${host}${c.port === 22 ? '' : `:${c.port}`}`
}

type Editing = { type: 'ssh' | 'webdav'; conn?: Connection } | null

function ConnectionsTab() {
  const { t } = useTranslation()
  const toast = useToast()
  const conns = useConnections()
  const test = useTestConnection()
  const del = useDeleteConnection()
  const [results, setResults] = useState<Record<string, TestResult | { error: string }>>({})
  const [testing, setTesting] = useState<string>()
  const [editing, setEditing] = useState<Editing>(null)
  const [deleting, setDeleting] = useState<Connection | null>(null)

  const runTest = (c: Connection) => {
    setTesting(c.id)
    test.mutate(c.id, {
      onSuccess: (r) => setResults((x) => ({ ...x, [c.id]: r })),
      onError: (e) => setResults((x) => ({ ...x, [c.id]: { error: errorText(t, e) } })),
      onSettled: () => setTesting(undefined),
    })
  }

  const inUseNames = del.error instanceof ApiError && del.error.code === 'in_use' ? del.error.message.split(': ').slice(1).join(': ') : ''

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap justify-end gap-2">
        <Button variant="utility" onClick={() => setEditing({ type: 'webdav' })}>
          {t('conn.new_webdav')}
        </Button>
        <Button variant="primary" onClick={() => setEditing({ type: 'ssh' })}>
          {t('conn.new_ssh')}
        </Button>
      </div>
      {conns.isError ? <Banner tone="danger">{errorText(t, conns.error)}</Banner> : null}
      <Table
        rows={conns.data ?? []}
        rowKey={(c) => c.id}
        empty={conns.isPending ? t('common.loading') : t('conn.empty')}
        renderBelow={(c) => {
          const r = results[c.id]
          if (!r) return null
          if ('error' in r) return <Banner tone="danger">{r.error}</Banner>
          return r.ok ? (
            <p className="flex items-center gap-2 text-caption">
              <StatusDot status="success" />
              {testMessage(t, r)}
            </p>
          ) : (
            <Banner tone="danger">{testMessage(t, r)}</Banner>
          )
        }}
        columns={[
          { key: 'name', header: t('common.name') },
          { key: 'type', header: t('conn.col_type'), render: (c) => t(`conn.type_${c.type}`) },
          { key: 'address', header: t('conn.col_address'), render: (c) => <span className="font-mono text-caption">{address(c)}</span>, title: address },
          {
            key: 'fp',
            header: t('conn.col_fingerprint'),
            render: (c) => <span className="font-mono text-caption">{c.host_key_fingerprint || '—'}</span>,
            title: (c) => c.host_key_fingerprint,
          },
          {
            key: 'actions',
            header: t('common.actions'),
            render: (c) => (
              <div className="flex gap-2">
                <Button onClick={() => runTest(c)} pending={testing === c.id}>
                  {t('conn.test')}
                </Button>
                <Button onClick={() => setEditing({ type: c.type, conn: c })}>{t('common.edit')}</Button>
                <Button onClick={() => setDeleting(c)}>{t('common.delete')}</Button>
              </div>
            ),
          },
        ]}
      />

      <Dialog
        open={editing !== null}
        onOpenChange={(o) => !o && setEditing(null)}
        wide
        title={t(editing?.conn ? `conn.edit_${editing.type}` : `conn.new_${editing?.type ?? 'ssh'}`)}
      >
        {editing?.type === 'ssh' ? (
          <SSHConnectionForm
            conn={editing.conn}
            onSaved={() => {
              toast(t('common.saved'))
              setEditing(null)
            }}
          />
        ) : null}
        {editing?.type === 'webdav' ? (
          <WebDAVConnectionForm
            conn={editing.conn}
            onSaved={() => {
              toast(t('common.saved'))
              setEditing(null)
            }}
          />
        ) : null}
      </Dialog>

      <Dialog
        open={deleting !== null}
        onOpenChange={(o) => {
          if (!o) {
            del.reset()
            setDeleting(null)
          }
        }}
        title={t('conn.delete_title')}
        footer={
          <Button
            variant="danger"
            pending={del.isPending}
            onClick={() =>
              deleting &&
              del.mutate(deleting.id, {
                onSuccess: () => {
                  toast(t('common.deleted'))
                  setDeleting(null)
                },
              })
            }
          >
            {t('common.delete')}
          </Button>
        }
      >
        <p>{t('conn.delete_confirm', { name: deleting?.name })}</p>
        {del.isError ? <Banner tone="danger">{inUseNames ? t('conn.in_use', { names: inUseNames }) : errorText(t, del.error)}</Banner> : null}
      </Dialog>
    </div>
  )
}

export function ConnectionsPage() {
  const { t } = useTranslation()
  const [params, setParams] = useSearchParams()
  const tab = params.get('tab') === 'keys' ? 'keys' : 'connections'
  return (
    <>
      <PageHeader title={t('conn.title')} />
      <Tabs
        value={tab}
        onValueChange={(v) => setParams(v === 'keys' ? { tab: 'keys' } : {}, { replace: true })}
        items={[
          { value: 'keys', label: t('conn.tab_keys'), content: <KeysTab /> },
          { value: 'connections', label: t('conn.tab_connections'), content: <ConnectionsTab /> },
        ]}
      />
    </>
  )
}
