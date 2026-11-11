import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useSearchParams } from 'react-router'
import { errorText } from '../../api/errors'
import { useRuns, useTasks } from '../../api/queries'
import type { Run } from '../../api/types'
import { formatBytes, formatDuration, type Lang } from '../../lib/format'
import { NodeTime } from '../../shell/NodeTime'
import { Banner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { Field } from '../../ui/Field'
import { PageHeader } from '../../ui/PageHeader'
import { Select } from '../../ui/Select'
import { StatusDot } from '../../ui/StatusDot'
import { Table } from '../../ui/Table'

export function runSeconds(r: Run, now = Date.now()): number {
  const end = r.finished_at ? Date.parse(r.finished_at) : now
  return Math.max(0, (end - Date.parse(r.started_at)) / 1000)
}

export function RunsPage() {
  const { t, i18n } = useTranslation()
  const [params, setParams] = useSearchParams()
  const taskId = params.get('task') ?? ''
  const tasks = useTasks()
  const runs = useRuns(taskId ? { task_id: taskId } : {})
  const rows = runs.data?.pages.flat() ?? []
  const lang = (i18n.language === 'zh' ? 'zh' : 'en') as Lang

  // load the next page when the end of the list scrolls into view
  const sentinel = useRef<HTMLDivElement>(null)
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = runs
  useEffect(() => {
    const el = sentinel.current
    if (!el || typeof IntersectionObserver === 'undefined') return
    const io = new IntersectionObserver((es) => {
      if (es.some((e) => e.isIntersecting) && hasNextPage && !isFetchingNextPage) void fetchNextPage()
    })
    io.observe(el)
    return () => io.disconnect()
  }, [hasNextPage, isFetchingNextPage, fetchNextPage])

  return (
    <>
      <PageHeader title={t('run.title')} />
      <div className="mb-4 max-w-xs">
        <Field label={t('run.filter_task')}>
          <Select
            value={taskId || 'all'}
            onValueChange={(v) => setParams(v === 'all' ? {} : { task: v }, { replace: true })}
            options={[{ value: 'all', label: t('run.all_tasks') }, ...(tasks.data ?? []).map((x) => ({ value: x.id, label: x.name }))]}
          />
        </Field>
      </div>
      {runs.isError ? <Banner tone="danger">{errorText(t, runs.error)}</Banner> : null}
      <Table
        rows={rows}
        rowKey={(r) => r.id}
        empty={runs.isPending ? t('common.loading') : t('run.empty')}
        columns={[
          {
            key: 'status',
            header: t('run.col_status'),
            render: (r) => (
              <Link to={`/runs/${r.id}`} className="hover:text-primary">
                <StatusDot status={r.status} withLabel />
              </Link>
            ),
          },
          { key: 'task_name', header: t('run.col_task') },
          { key: 'kind', header: t('run.col_kind'), render: (r) => t(`run.kind.${r.kind}`) },
          { key: 'trigger', header: t('run.col_trigger'), render: (r) => t(`run.trigger.${r.trigger}`) },
          { key: 'started', header: t('run.col_started'), render: (r) => <NodeTime iso={r.started_at} /> },
          { key: 'duration', header: t('run.col_duration'), render: (r) => formatDuration(runSeconds(r), lang) },
          { key: 'bytes', header: t('run.col_bytes'), render: (r) => formatBytes(r.bytes) },
          { key: 'files', header: t('run.col_files'), render: (r) => String(r.files) },
        ]}
      />
      <div ref={sentinel} className="mt-4 flex justify-center">
        {hasNextPage ? (
          <Button onClick={() => void fetchNextPage()} pending={isFetchingNextPage}>
            {t('run.load_more')}
          </Button>
        ) : null}
      </div>
    </>
  )
}
