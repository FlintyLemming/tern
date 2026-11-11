import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { errorText } from '../../api/errors'
import { useOverview, useRunProgress, useRunTask } from '../../api/queries'
import type { OverviewItem } from '../../api/types'
import { formatBytes, formatETA, formatRelative, formatSpeed, type Lang } from '../../lib/format'
import { NodeTime, useFormatTime, useNodeZone } from '../../shell/NodeTime'
import { Banner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { PageHeader } from '../../ui/PageHeader'
import { ProgressBar } from '../../ui/ProgressBar'
import { StatusDot } from '../../ui/StatusDot'
import { Table } from '../../ui/Table'
import { useToast } from '../../ui/Toast'
import { BBRCard } from './BBRCard'

// useNow ticks once a minute, for "3 分钟前".
function useNow() {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(id)
  }, [])
  return now
}

// ActiveRun is the progress line under a running task. Progress arrives by
// run.progress events; the overview's own copy covers the time before the first.
function ActiveRun({ item }: { item: OverviewItem }) {
  const { t } = useTranslation()
  const ar = item.active_run!
  const live = useRunProgress(ar.run_id)
  const p = live ?? ar.progress
  if (ar.status === 'retry_wait') return <p className="text-caption text-ink-muted">{t('overview.waiting_retry')}</p>
  if (ar.status === 'queued') return <p className="text-caption text-ink-muted">{t('overview.queued')}</p>
  if (!p) return <p className="text-caption text-ink-muted">{t('overview.starting')}</p>
  return (
    <div className="flex flex-col gap-1">
      <ProgressBar done={p.bytes_done} total={p.bytes_total} />
      <p className="text-caption text-ink-muted">
        {t('overview.progress', { done: formatBytes(p.bytes_done), total: formatBytes(p.bytes_total), speed: formatSpeed(p.speed_bps), eta: formatETA(p.eta_sec) })}
      </p>
    </div>
  )
}

export function OverviewPage() {
  const { t, i18n } = useTranslation()
  const toast = useToast()
  const ov = useOverview()
  const run = useRunTask()
  const now = useNow()
  const fmt = useFormatTime()
  const zone = useNodeZone()
  const tasks = ov.data?.tasks ?? []
  const stale = tasks.filter((x) => x.stale)
  const checkFailed = tasks.filter((x) => x.last_check_status === 'failed')
  const lang = (i18n.language === 'zh' ? 'zh' : 'en') as Lang

  return (
    <>
      <PageHeader title={t('overview.title')} />
      <div className="flex flex-col gap-6">
        {ov.isError ? <Banner tone="danger">{errorText(t, ov.error)}</Banner> : null}

        {stale.length > 0 ? (
          <Banner tone="danger">
            <p className="font-semibold">{t('overview.stale', { count: stale.length })}</p>
            <ul className="mt-2 flex flex-col gap-1">
              {stale.map((x) => (
                <li key={x.task_id} className="flex flex-wrap gap-x-3">
                  <Link to={`/tasks/${x.task_id}`} className="text-primary underline">
                    {x.name}
                  </Link>
                  <span className="text-caption text-ink-muted">
                    {t('overview.last_success_at', { when: x.last_success_at ? fmt(x.last_success_at) : t('common.never') })}
                  </span>
                </li>
              ))}
            </ul>
          </Banner>
        ) : null}

        {checkFailed.map((x) => (
          <Banner
            key={x.task_id}
            tone="danger"
            action={
              <Link to={`/runs?task=${encodeURIComponent(x.task_id)}`} className="text-body text-primary underline">
                {t('overview.check_failed_link')}
              </Link>
            }
          >
            <p className="font-semibold">{t('overview.check_failed', { name: x.name })}</p>
          </Banner>
        ))}

        <Table
          rows={tasks}
          rowKey={(x) => x.task_id}
          empty={ov.isPending ? t('common.loading') : t('overview.empty')}
          renderBelow={(x) => (x.active_run ? <ActiveRun item={x} /> : null)}
          columns={[
            {
              key: 'name',
              header: t('common.name'),
              title: (x) => x.name,
              render: (x) => {
                const st = x.active_run?.status ?? (x.last_status || undefined)
                return (
                  <span className="inline-flex items-center gap-2">
                    {st ? <StatusDot status={st} /> : <span className="inline-block size-2" />}
                    <Link to={`/tasks/${x.task_id}`} className="font-semibold text-ink hover:text-primary">
                      {x.name}
                    </Link>
                  </span>
                )
              },
            },
            { key: 'type', header: t('task.col_type'), render: (x) => t(`task.type_${x.type}`) },
            {
              key: 'last',
              header: t('task.col_last'),
              render: (x) => (x.last_status ? <StatusDot status={x.last_status} withLabel /> : <span className="text-ink-muted">{t('task.never_ran')}</span>),
            },
            {
              key: 'success',
              header: t('overview.col_last_success'),
              title: (x) => (x.last_success_at && zone ? `${fmt(x.last_success_at)} ${zone}` : undefined),
              render: (x) =>
                x.last_success_at ? (
                  <span className={x.stale ? 'font-semibold text-danger' : undefined}>{formatRelative(x.last_success_at, now, lang)}</span>
                ) : (
                  <span className={x.stale ? 'font-semibold text-danger' : 'text-ink-muted'}>{t('common.never')}</span>
                ),
            },
            { key: 'next', header: t('overview.col_next'), render: (x) => <NodeTime iso={x.next_run_at} /> },
            {
              key: 'actions',
              header: t('common.actions'),
              render: (x) => (
                <Button
                  pending={run.isPending && run.variables?.id === x.task_id}
                  disabled={Boolean(x.active_run)}
                  onClick={() =>
                    run.mutate(
                      { id: x.task_id },
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
                      },
                    )
                  }
                >
                  {t('task.run_now')}
                </Button>
              ),
            },
          ]}
        />

        <BBRCard />
      </div>
    </>
  )
}
