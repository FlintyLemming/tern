import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate } from 'react-router'
import { errorText } from '../../api/errors'
import { useOverview, useRunTask, useSetTaskEnabled, useTasks } from '../../api/queries'
import type { OverviewItem, RunKind, Task } from '../../api/types'
import { NodeTime } from '../../shell/NodeTime'
import { Banner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { Dialog } from '../../ui/Dialog'
import { PageHeader } from '../../ui/PageHeader'
import { StatusDot } from '../../ui/StatusDot'
import { Switch } from '../../ui/Switch'
import { Table } from '../../ui/Table'
import { useToast } from '../../ui/Toast'
import { CommandLineDialog } from './CommandLineDialog'

export function TasksPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const toast = useToast()
  const tasks = useTasks()
  const overview = useOverview()
  const run = useRunTask()
  const enable = useSetTaskEnabled()
  const [choosing, setChoosing] = useState(false)
  const [cmdFor, setCmdFor] = useState<string | null>(null)
  const [starting, setStarting] = useState<string>()

  const ov = new Map<string, OverviewItem>((overview.data?.tasks ?? []).map((o) => [o.task_id, o]))

  const start = (task: Task, kind?: RunKind) => {
    setStarting(`${task.id}/${kind ?? ''}`)
    run.mutate(
      { id: task.id, kind },
      {
        onSuccess: (r) =>
          toast(
            <span>
              {t('task.queued')} ·{' '}
              <Link className="text-primary underline" to={`/runs/${r.run_id}`}>
                {t('task.view_run')}
              </Link>
            </span>,
          ),
        onError: (e) => toast(errorText(t, e), 'danger'),
        onSettled: () => setStarting(undefined),
      },
    )
  }

  return (
    <>
      <PageHeader
        title={t('task.title')}
        actions={
          <Button variant="primary" onClick={() => setChoosing(true)}>
            {t('task.new')}
          </Button>
        }
      />
      {tasks.isError ? <Banner tone="danger">{errorText(t, tasks.error)}</Banner> : null}
      <Table
        rows={tasks.data ?? []}
        rowKey={(x) => x.id}
        empty={tasks.isPending ? t('common.loading') : t('task.empty')}
        columns={[
          {
            key: 'name',
            header: t('common.name'),
            title: (x) => x.name,
            render: (x) => {
              const o = ov.get(x.id)
              const st = o?.active_run?.status ?? (o?.last_status || undefined)
              return (
                <span className="inline-flex items-center gap-2">
                  {st ? <StatusDot status={st} /> : <span className="inline-block size-2" />}
                  <Link to={`/tasks/${x.id}`} className="font-semibold text-ink hover:text-primary">
                    {x.name}
                  </Link>
                </span>
              )
            },
          },
          { key: 'type', header: t('task.col_type'), render: (x) => t(`task.type_${x.type}`) },
          {
            key: 'schedule',
            header: t('task.col_schedule'),
            render: (x) => {
              const next = ov.get(x.id)?.next_run_at
              return x.schedule ? (
                <span className="flex flex-col">
                  <span className="font-mono text-caption text-ink-muted">{x.schedule}</span>
                  {next ? <NodeTime iso={next} /> : null}
                </span>
              ) : (
                <span className="text-ink-muted">{t('task.manual_only')}</span>
              )
            },
          },
          {
            key: 'last',
            header: t('task.col_last'),
            render: (x) => {
              const s = ov.get(x.id)?.last_status
              return s ? <StatusDot status={s} withLabel /> : <span className="text-ink-muted">{t('task.never_ran')}</span>
            },
          },
          {
            key: 'enabled',
            header: t('task.col_enabled'),
            render: (x) => (
              <Switch
                checked={x.enabled}
                aria-label={t('task.enabled')}
                disabled={enable.isPending && enable.variables?.id === x.id}
                onCheckedChange={(v) => enable.mutate({ id: x.id, enabled: v }, { onError: (e) => toast(errorText(t, e), 'danger') })}
              />
            ),
          },
          {
            key: 'actions',
            header: t('common.actions'),
            render: (x) => (
              <div className="flex flex-wrap gap-2">
                <Button onClick={() => start(x)} pending={starting === `${x.id}/`}>
                  {t('task.run_now')}
                </Button>
                {x.type === 'sync' ? (
                  <Button onClick={() => start(x, 'dry_run')} pending={starting === `${x.id}/dry_run`}>
                    {t('task.dry_run')}
                  </Button>
                ) : null}
                <Button onClick={() => navigate(`/tasks/${x.id}`)}>{t('common.edit')}</Button>
                <Button onClick={() => setCmdFor(x.id)}>{t('task.command_line')}</Button>
              </div>
            ),
          },
        ]}
      />

      <Dialog open={choosing} onOpenChange={setChoosing} title={t('task.new_choose')}>
        {(['backup', 'sync'] as const).map((type) => (
          <button
            key={type}
            type="button"
            onClick={() => navigate(`/tasks/new?type=${type}`)}
            className="flex flex-col gap-1 rounded-lg border border-hairline p-4 text-left hover:border-primary"
          >
            <span className="text-title">{t(`task.type_${type}`)}</span>
            <span className="text-caption text-ink-muted">{t(`task.type_${type}_desc`)}</span>
          </button>
        ))}
      </Dialog>
      <CommandLineDialog taskId={cmdFor} onClose={() => setCmdFor(null)} />
    </>
  )
}
