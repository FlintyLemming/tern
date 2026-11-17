import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate, useParams } from 'react-router'
import { ApiError } from '../../api/client'
import { errorText } from '../../api/errors'
import { qk, useCancelRun, useRun, useRunProgress, useRunTask } from '../../api/queries'
import { isTerminal, type Run } from '../../api/types'
import { formatBytes, formatDuration, type Lang } from '../../lib/format'
import { NodeTime } from '../../shell/NodeTime'
import { Banner } from '../../ui/Banner'
import { Button } from '../../ui/Button'
import { Card } from '../../ui/Card'
import { Dialog } from '../../ui/Dialog'
import { PageHeader } from '../../ui/PageHeader'
import { RunProgress } from '../../ui/ProgressBar'
import { StatusDot } from '../../ui/StatusDot'
import { Table } from '../../ui/Table'
import { useToast } from '../../ui/Toast'
import { LogViewer } from './LogViewer'
import { runSeconds } from './RunsPage'

// A mirror sync stopped by its deletion limit can be allowed once; the
// guard that stops on an empty or missing source cannot.
const deleteLimitHit = (r: Run) => r.error_kind === 'guard' && r.error_summary.includes('would delete')

export function RunDetailPage() {
  const { id = '' } = useParams()
  const { t, i18n } = useTranslation()
  const navigate = useNavigate()
  const toast = useToast()
  const qc = useQueryClient()
  const q = useRun(id)
  const progress = useRunProgress(id)
  const cancel = useCancelRun()
  const rerun = useRunTask()
  const [allowing, setAllowing] = useState(false)
  const lang = (i18n.language === 'zh' ? 'zh' : 'en') as Lang

  if (q.isError) {
    return (
      <>
        <PageHeader title={t('run.detail')} />
        <Banner tone="danger">{q.error instanceof ApiError && q.error.status === 404 ? t('run.not_found') : errorText(t, q.error)}</Banner>
      </>
    )
  }
  const r = q.data
  if (!r) return <p className="text-caption text-ink-muted">{t('common.loading')}</p>
  const done = isTerminal(r.status)

  return (
    <>
      <PageHeader
        title={`${r.task_name} · ${t(`run.kind.${r.kind}`)}`}
        description={<Link to={`/tasks/${r.task_id}`} className="text-primary underline">{t('run.task_link')}</Link>}
        actions={
          <>
            {!done ? (
              <Button
                variant="danger"
                pending={cancel.isPending}
                onClick={() => cancel.mutate(r.id, { onSuccess: () => toast(t('run.cancel_sent')), onError: (e) => toast(errorText(t, e), 'danger') })}
              >
                {t('run.cancel')}
              </Button>
            ) : null}
            {deleteLimitHit(r) ? (
              <Button variant="primary" onClick={() => setAllowing(true)}>
                {t('run.allow_once')}
              </Button>
            ) : null}
          </>
        }
      />
      <div className="flex flex-col gap-6">
        <Card>
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 sm:grid-cols-[auto_1fr_auto_1fr]">
            <dt className="text-ink-muted">{t('run.col_status')}</dt>
            <dd>
              <StatusDot status={r.status} withLabel />
            </dd>
            <dt className="text-ink-muted">{t('run.col_trigger')}</dt>
            <dd>{t(`run.trigger.${r.trigger}`)}</dd>
            <dt className="text-ink-muted">{t('run.started')}</dt>
            <dd>
              <NodeTime iso={r.started_at} />
            </dd>
            <dt className="text-ink-muted">{t('run.finished')}</dt>
            <dd>
              <NodeTime iso={r.finished_at} />
            </dd>
            <dt className="text-ink-muted">{t('run.col_duration')}</dt>
            <dd>{formatDuration(runSeconds(r), lang)}</dd>
            <dt className="text-ink-muted">{t('run.attempts')}</dt>
            <dd>{r.attempts}</dd>
            <dt className="text-ink-muted">{t('run.col_bytes')}</dt>
            <dd>{formatBytes(r.bytes)}</dd>
            <dt className="text-ink-muted">{t('run.col_files')}</dt>
            <dd>{r.files}</dd>
          </dl>
          {!done && progress ? (
            <div className="mt-4 flex flex-col gap-1">
              <RunProgress progress={progress} />
            </div>
          ) : null}
        </Card>

        {r.error_kind ? (
          <Banner tone="danger">
            <p className="font-semibold">{t(`kind.${r.error_kind}`)}</p>
            {r.error_summary ? <p className="mt-1 break-words">{r.error_summary}</p> : null}
          </Banner>
        ) : null}

        {r.units.length ? (
          <div className="flex flex-col gap-2">
            <h2 className="text-title">{t('run.units')}</h2>
            <Table
              rows={r.units}
              rowKey={(u) => u.name}
              columns={[
                { key: 'name', header: t('run.unit_name') },
                { key: 'status', header: t('run.col_status'), render: (u) => <StatusDot status={u.status} withLabel /> },
                { key: 'bytes', header: t('run.col_bytes'), render: (u) => formatBytes(u.bytes) },
                { key: 'files', header: t('run.col_files'), render: (u) => String(u.files) },
                { key: 'error', header: t('run.unit_error'), render: (u) => <span className="text-danger">{u.error}</span>, title: (u) => u.error },
              ]}
            />
          </div>
        ) : null}

        <LogViewer
          key={r.id}
          runId={r.id}
          onEOF={() => {
            // the log is complete only when the run is over: catch up if an event was missed
            if (!done) void qc.invalidateQueries({ queryKey: qk.run(r.id) })
          }}
        />
      </div>

      <Dialog
        open={allowing}
        onOpenChange={setAllowing}
        title={t('run.allow_title')}
        footer={
          <Button
            variant="danger"
            pending={rerun.isPending}
            onClick={() =>
              rerun.mutate(
                { id: r.task_id, allow_deletes_once: true },
                {
                  onSuccess: (x) => {
                    setAllowing(false)
                    navigate(`/runs/${x.run_id}`)
                  },
                  onError: (e) => toast(errorText(t, e), 'danger'),
                },
              )
            }
          >
            {t('run.allow_confirm')}
          </Button>
        }
      >
        <p>{t('run.allow_body')}</p>
        <p className="rounded-md border border-danger px-3 py-2 font-mono text-caption">{r.error_summary}</p>
      </Dialog>
    </>
  )
}
