import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router'
import { errorText } from '../../api/errors'
import { useSnapshots, useTasks } from '../../api/queries'
import type { Snapshot } from '../../api/types'
import { NodeTime } from '../../shell/NodeTime'
import { Banner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { EmptyState } from '../../ui/EmptyState'
import { Field } from '../../ui/Field'
import { PageHeader } from '../../ui/PageHeader'
import { Select } from '../../ui/Select'
import { Table } from '../../ui/Table'
import { RestoreDialog } from './RestoreDialog'

export function SnapshotsPage() {
  const { t } = useTranslation()
  const [params, setParams] = useSearchParams()
  const tasks = useTasks()
  const backups = (tasks.data ?? []).filter((x) => x.type === 'backup')
  const taskId = params.get('task') ?? ''
  const snaps = useSnapshots(taskId)
  const [restoring, setRestoring] = useState<Snapshot | null>(null)

  // with no task in the address, start with the first backup task
  useEffect(() => {
    if (!taskId && backups.length > 0) setParams({ task: backups[0].id }, { replace: true })
  }, [taskId, backups, setParams])

  const rows = [...(snaps.data?.snapshots ?? [])].sort((a, b) => Date.parse(b.time) - Date.parse(a.time))
  const total = snaps.data?.total ?? 0

  return (
    <>
      <PageHeader title={t('snap.title')} />
      <div className="mb-4 max-w-xs">
        <Field label={t('snap.task')}>
          <Select
            value={taskId}
            onValueChange={(v) => setParams({ task: v }, { replace: true })}
            placeholder={t('snap.task_placeholder')}
            options={backups.map((x) => ({ value: x.id, label: x.name }))}
          />
        </Field>
      </div>
      {tasks.data && backups.length === 0 ? <EmptyState>{t('snap.no_backup_tasks')}</EmptyState> : null}
      {snaps.isError ? <Banner tone="danger">{errorText(t, snaps.error)}</Banner> : null}
      {taskId && snaps.isPending ? <p className="text-caption text-ink-muted">{t('snap.loading')}</p> : null}
      {snaps.data ? (
        <div className="flex flex-col gap-2">
          {total > rows.length ? <p className="text-caption text-ink-muted">{t('snap.capped', { shown: rows.length, total })}</p> : null}
          <Table
            rows={rows}
            rowKey={(s) => s.id}
            empty={t('snap.empty')}
            columns={[
              { key: 'time', header: t('snap.col_time'), render: (s) => <NodeTime iso={s.time} /> },
              { key: 'short_id', header: t('snap.col_id'), render: (s) => <span className="font-mono">{s.short_id}</span> },
              { key: 'host', header: t('snap.col_host') },
              { key: 'paths', header: t('snap.col_paths'), render: (s) => s.paths.join(', '), title: (s) => s.paths.join('\n') },
              {
                key: 'mine',
                header: t('snap.col_mine'),
                render: (s) =>
                  s.mine ? <span className="rounded-full border border-hairline bg-surface px-2 py-1 text-eyebrow text-primary">{t('snap.mine')}</span> : null,
              },
              { key: 'actions', header: t('common.actions'), render: (s) => <Button onClick={() => setRestoring(s)}>{t('snap.restore')}</Button> },
            ]}
          />
        </div>
      ) : null}
      <RestoreDialog taskId={taskId} repository={snaps.data?.repository ?? ''} snap={restoring} onClose={() => setRestoring(null)} />
    </>
  )
}
