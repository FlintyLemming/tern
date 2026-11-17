import { Plus, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import type { Connection, Protocol, SyncSpec } from '../../api/types'
import { PathInput } from '../../browse/FolderPicker'
import { Button } from '../../ui/Button'
import { Field } from '../../ui/Field'
import { Select } from '../../ui/Select'
import { ExcludesField, NumField, useFieldError } from './form'

const connType = (p: Protocol) => (p === 'webdav' ? 'webdav' : 'ssh')

export function SyncFields({ s, set, conns }: { s: SyncSpec; set: (p: Partial<SyncSpec>) => void; conns: Connection[] }) {
  const { t } = useTranslation()
  const fe = useFieldError()
  const usable = conns.filter((c) => c.type === connType(s.protocol))
  const pairs = s.pairs.length ? s.pairs : [{ local: '', remote: '' }]
  const conn = usable.find((c) => c.id === s.connection_id)

  const setProtocol = (p: Protocol) => {
    const keepConn = conns.find((c) => c.id === s.connection_id)?.type === connType(p)
    set({
      protocol: p,
      ...(keepConn ? {} : { connection_id: '' }),
      // rsync moves one file at a time: the server wants 0 there
      transfers: p === 'rsync' ? 0 : s.transfers > 0 ? s.transfers : 4,
    })
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label={t('task.protocol')} hint={t('task.protocol_hint')} error={fe('sync.protocol')}>
          <Select
            value={s.protocol}
            onValueChange={(v) => setProtocol(v as Protocol)}
            options={(['sftp', 'rsync', 'webdav'] as const).map((p) => ({ value: p, label: t(`task.protocol_${p}`) }))}
          />
        </Field>
        <Field label={t('task.conn')} error={fe('sync.connection_id')}>
          <Select
            value={s.connection_id}
            onValueChange={(v) => set({ connection_id: v })}
            placeholder={t('task.conn_placeholder')}
            options={usable.map((c) => ({ value: c.id, label: c.name }))}
          />
        </Field>
        <Field label={t('task.direction')} error={fe('sync.direction')}>
          <Select
            value={s.direction}
            onValueChange={(v) => set({ direction: v as SyncSpec['direction'] })}
            options={[
              { value: 'push', label: t('task.direction_push') },
              { value: 'pull', label: t('task.direction_pull') },
            ]}
          />
        </Field>
      </div>

      <div className="flex flex-col gap-2">
        <p className="text-eyebrow text-ink-secondary">{t('task.pairs')}</p>
        {pairs.map((p, i) => (
          <div key={i} data-testid="pair" className="flex items-start gap-2">
            <div className="grid flex-1 gap-2 sm:grid-cols-2">
              <Field label={t('task.pair_local')} error={fe(`sync.pairs[${i}].local`)}>
                <PathInput
                  value={p.local}
                  placeholder={t('task.local_placeholder')}
                  source={{ kind: 'local' }}
                  onChange={(v) => set({ pairs: pairs.map((x, j) => (j === i ? { ...x, local: v } : x)) })}
                />
              </Field>
              <Field label={t('task.pair_remote')} error={fe(`sync.pairs[${i}].remote`)}>
                <PathInput
                  value={p.remote}
                  source={conn ? { kind: 'connection', id: conn.id, type: conn.type === 'webdav' ? 'webdav' : 'ssh', name: conn.name } : null}
                  unavailable={t('browse.need_conn')}
                  onChange={(v) => set({ pairs: pairs.map((x, j) => (j === i ? { ...x, remote: v } : x)) })}
                />
              </Field>
            </div>
            <Button aria-label={t('common.remove')} disabled={pairs.length === 1} onClick={() => set({ pairs: pairs.filter((_, j) => j !== i) })} className="mt-5">
              <X aria-hidden className="size-4" />
            </Button>
          </div>
        ))}
        {fe('sync.pairs') ? <p className="text-caption text-danger">{fe('sync.pairs')}</p> : null}
        <Button icon={<Plus aria-hidden className="size-4" />} onClick={() => set({ pairs: [...pairs, { local: '', remote: '' }] })} className="self-start">
          {t('task.add_pair')}
        </Button>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label={t('task.delete_mode')} error={fe('sync.delete_mode')}>
          <Select
            value={s.delete_mode}
            onValueChange={(v) => set({ delete_mode: v as SyncSpec['delete_mode'] })}
            options={[
              { value: 'keep', label: t('task.delete_keep') },
              { value: 'mirror', label: t('task.delete_mirror') },
            ]}
          />
        </Field>
        {s.delete_mode === 'mirror' ? (
          <NumField
            label={t('task.max_deletes')}
            hint={t('task.max_deletes_hint')}
            error={fe('sync.max_deletes')}
            value={s.max_deletes}
            onChange={(n) => set({ max_deletes: n })}
          />
        ) : null}
        {s.delete_mode === 'mirror' ? <p className="text-body font-semibold text-danger sm:col-span-2">{t('task.mirror_warning')}</p> : null}
        <Field label={t('task.compare')} error={fe('sync.compare')}>
          <Select
            value={s.compare}
            onValueChange={(v) => set({ compare: v as SyncSpec['compare'] })}
            options={[
              { value: 'size_mtime', label: t('task.compare_size_mtime') },
              { value: 'checksum', label: t('task.compare_checksum') },
            ]}
          />
        </Field>
      </div>

      <ExcludesField prefix="sync.excludes" value={s.excludes} onChange={(excludes) => set({ excludes })} />

      <div className="grid gap-4 sm:grid-cols-2">
        <NumField label={t('task.limit_kib')} hint={t('task.limit_hint')} error={fe('sync.limit_kib')} value={s.limit_kib} emptyZero onChange={(n) => set({ limit_kib: n })} />
        {s.protocol !== 'rsync' ? (
          <NumField label={t('task.transfers')} error={fe('sync.transfers')} value={s.transfers} onChange={(n) => set({ transfers: n })} />
        ) : null}
      </div>
    </div>
  )
}
