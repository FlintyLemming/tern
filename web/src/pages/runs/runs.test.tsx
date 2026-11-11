import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Route } from 'react-router'
import { afterEach } from 'vitest'
import type { Run } from '../../api/types'
import { fakeFetch, type Reply } from '../../test/fetch'
import { backupTask, run, syncTask, system } from '../../test/data'
import { Loc, renderRoutes } from '../../test/render'
import { choose } from '../../test/select'
import { RunDetailPage } from './RunDetailPage'
import { RunsPage } from './RunsPage'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

const eofLog = { body: { data: 'done\n', next_offset: 5, eof: true } }

function renderAt(path: string, routes: Record<string, Reply | Reply[]>, user?: ReturnType<typeof userEvent.setup>) {
  const f = fakeFetch({
    'GET /api/v1/system': { body: system },
    'GET /api/v1/tasks': { body: [backupTask, syncTask] },
    ...routes,
  })
  const r = renderRoutes(
    <>
      <Route path="/runs" element={<RunsPage />} />
      <Route path="/runs/:id" element={<RunDetailPage />} />
      <Route path="*" element={<Loc />} />
    </>,
    path,
    { user },
  )
  return { f, ...r }
}

const guardRun = (summary: string): Run =>
  run({ id: 'r5', status: 'failed', error_kind: 'guard', error_summary: summary, units: [{ name: '/data/photos', status: 'failed', bytes: 0, files: 0, error: summary }] })

it('detail: units table', async () => {
  renderAt('/runs/r1', {
    'GET /api/v1/runs/r1': {
      body: run({
        status: 'partial',
        error_kind: 'network',
        error_summary: '1 of 2 folder pairs failed',
        units: [
          { name: '/data/photos', status: 'success', bytes: 2048, files: 2, error: '' },
          { name: '/data/videos', status: 'failed', bytes: 0, files: 0, error: 'connection reset by peer' },
        ],
      }),
    },
    'GET /api/v1/runs/r1/log': eofLog,
  })
  const ok = (await screen.findByText('/data/photos')).closest('tr')!
  const bad = screen.getByText('/data/videos').closest('tr')!
  expect(within(ok).getByText('成功')).toBeInTheDocument()
  expect(within(ok).getByText('2 KiB')).toBeInTheDocument()
  expect(within(bad).getByText('失败')).toBeInTheDocument()
  expect(within(bad).getByText('connection reset by peer')).toBeInTheDocument()
  expect(screen.getByText('网络问题')).toBeInTheDocument()
  expect(screen.getByText('1 of 2 folder pairs failed')).toBeInTheDocument()
})

it('detail: cancel only while running', async () => {
  const a = renderAt('/runs/r1', {
    'GET /api/v1/runs/r1': { body: run({ status: 'running', finished_at: null }) },
    'GET /api/v1/runs/r1/log': { body: { data: '', next_offset: 0, eof: false } },
    'POST /api/v1/runs/r1/cancel': { status: 202 },
  })
  await a.user.click(await screen.findByRole('button', { name: '取消运行' }))
  await waitFor(() => expect(a.f.of('POST', '/api/v1/runs/r1/cancel')).toHaveLength(1))
  a.unmount()
  vi.unstubAllGlobals()
  renderAt('/runs/r1', { 'GET /api/v1/runs/r1': { body: run({ status: 'success' }) }, 'GET /api/v1/runs/r1/log': eofLog })
  await screen.findByText('/data/photos', {}, { timeout: 50 }).catch(() => {})
  await screen.findAllByText('成功')
  expect(screen.queryByRole('button', { name: '取消运行' })).toBeNull()
})

it('detail: allow-once button only for delete-limit guard', async () => {
  const a = renderAt('/runs/r5', { 'GET /api/v1/runs/r5': { body: guardRun('would delete 101 files (limit 100)') }, 'GET /api/v1/runs/r5/log': eofLog })
  expect(await screen.findByRole('button', { name: '本次允许并重跑' })).toBeInTheDocument()
  a.unmount()
  vi.unstubAllGlobals()
  renderAt('/runs/r5', {
    'GET /api/v1/runs/r5': { body: guardRun('source is missing or empty: /data/photos; nothing was changed') },
    'GET /api/v1/runs/r5/log': eofLog,
  })
  await screen.findByText(/source is missing or empty/, { selector: 'p' })
  expect(screen.queryByRole('button', { name: '本次允许并重跑' })).toBeNull()
})

it('detail: allow-once confirms then reruns', async () => {
  const { f, user } = renderAt('/runs/r5', {
    'GET /api/v1/runs/r5': { body: guardRun('would delete 101 files (limit 100)') },
    'GET /api/v1/runs/r5/log': eofLog,
    'POST /api/v1/tasks/t2/run': { status: 202, body: { run_id: 'r6' } },
    'GET /api/v1/runs/r6': { body: run({ id: 'r6', status: 'queued', finished_at: null }) },
    'GET /api/v1/runs/r6/log': { body: { data: '', next_offset: 0, eof: false } },
  })
  await user.click(await screen.findByRole('button', { name: '本次允许并重跑' }))
  const dlg = await screen.findByRole('dialog')
  expect(dlg).toHaveTextContent('would delete 101 files (limit 100)')
  expect(f.of('POST', '/api/v1/tasks/t2/run')).toHaveLength(0)
  await user.click(within(dlg).getByRole('button', { name: '允许并重跑' }))
  await waitFor(() => expect(f.of('POST', '/api/v1/tasks/t2/run')).toHaveLength(1))
  expect(f.of('POST', '/api/v1/tasks/t2/run')[0].body).toEqual({ allow_deletes_once: true })
  await waitFor(() => expect(f.of('GET', '/api/v1/runs/r6').length).toBeGreaterThan(0))
})

it('log: polls with next_offset until eof', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime.bind(vi) })
  const { f } = renderAt(
    '/runs/r1',
    {
      'GET /api/v1/runs/r1': { body: run({ status: 'running', finished_at: null }) },
      'GET /api/v1/runs/r1/log': [
        { body: { data: 'one\n', next_offset: 4, eof: false } },
        { body: { data: 'two\n', next_offset: 8, eof: false } },
        { body: { data: 'three\n', next_offset: 14, eof: true } },
      ],
    },
    user,
  )
  expect(await screen.findByText('one')).toBeInTheDocument()
  await act(async () => vi.advanceTimersByTime(1000))
  expect(await screen.findByText('two')).toBeInTheDocument()
  await act(async () => vi.advanceTimersByTime(1000))
  expect(await screen.findByText('three')).toBeInTheDocument()
  await act(async () => vi.advanceTimersByTime(5000))
  const offsets = f.of('GET', '/api/v1/runs/r1/log').map((c) => new URL(c.url, 'http://x').searchParams.get('offset'))
  expect(offsets).toEqual(['0', '4', '8'])
})

it('log: keeps last 5000 lines', async () => {
  const data = Array.from({ length: 6000 }, (_, i) => `line ${i + 1}`).join('\n') + '\n'
  const { container } = renderAt('/runs/r1', {
    'GET /api/v1/runs/r1': { body: run() },
    'GET /api/v1/runs/r1/log': { body: { data, next_offset: data.length, eof: true } },
  })
  expect(await screen.findByText('前面的日志已折叠')).toBeInTheDocument()
  expect(container.querySelectorAll('[data-log-line]')).toHaveLength(5000)
  expect(screen.getByText('line 6000')).toBeInTheDocument()
  expect(screen.queryByText('line 1000')).toBeNull()
})

it('log: download fetches from offset 0', async () => {
  const blobs: Blob[] = []
  vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: (b: Blob) => (blobs.push(b), 'blob:x'), revokeObjectURL: () => {} }))
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  const { f, user } = renderAt('/runs/r1', {
    'GET /api/v1/runs/r1': { body: run() },
    'GET /api/v1/runs/r1/log?offset=0': [{ body: { data: 'tail\n', next_offset: 5, eof: true } }, { body: { data: 'first part\n', next_offset: 11, eof: false } }],
    'GET /api/v1/runs/r1/log?offset=11': { body: { data: 'second part\n', next_offset: 23, eof: true } },
  })
  await screen.findByText('tail')
  await user.click(screen.getByRole('button', { name: '下载完整日志' }))
  await waitFor(() => expect(blobs).toHaveLength(1))
  expect(await blobs[0].text()).toBe('first part\nsecond part\n')
  const offsets = f.of('GET', '/api/v1/runs/r1/log').map((c) => new URL(c.url, 'http://x').searchParams.get('offset'))
  expect(offsets).toEqual(['0', '0', '11'])
})

it('list: filter by task and paginate', async () => {
  const page1 = Array.from({ length: 50 }, (_, i) =>
    run({ id: `p${i}`, task_id: 't1', task_name: 'docs', kind: 'backup', started_at: new Date(Date.parse('2026-11-10T19:00:00Z') - i * 3600_000).toISOString().replace('.000', '') }),
  )
  const last = page1[49].started_at
  const { f, user } = renderAt('/runs?task=t1', {
    [`GET /api/v1/runs?limit=50&task_id=t1`]: { body: page1 },
    [`GET /api/v1/runs?limit=50&task_id=t1&before=${encodeURIComponent(last)}`]: { body: [run({ id: 'older', task_id: 't1', task_name: 'docs', kind: 'check' })] },
    'GET /api/v1/runs?limit=50': { body: [run()] },
  })
  await waitFor(() => expect(screen.getAllByText('docs').length).toBeGreaterThanOrEqual(50))
  await user.click(screen.getByRole('button', { name: '加载更多' }))
  expect(await screen.findByText('校验')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: '加载更多' })).toBeNull()
  await choose(user, '任务', '全部任务')
  await waitFor(() => expect(f.of('GET', '/api/v1/runs').some((c) => c.url === '/api/v1/runs?limit=50')).toBe(true))
  expect(await screen.findByText('photos')).toBeInTheDocument()
})

it('list: columns', async () => {
  renderAt('/runs', {
    'GET /api/v1/runs?limit=50': { body: [run({ trigger: 'schedule', bytes: 1536, files: 3, started_at: '2026-11-10T19:00:00Z', finished_at: '2026-11-10T19:01:05Z' })] },
  })
  const row = (await screen.findByText('photos')).closest('tr')!
  expect(within(row).getByText('成功')).toBeInTheDocument()
  expect(within(row).getByText('同步')).toBeInTheDocument()
  expect(within(row).getByText('计划')).toBeInTheDocument()
  expect(await within(row).findByText('2026-11-11 03:00')).toBeInTheDocument()
  expect(within(row).getByText('1 分 5 秒')).toBeInTheDocument()
  expect(within(row).getByText('1.5 KiB')).toBeInTheDocument()
  expect(within(row).getByText('3')).toBeInTheDocument()
  expect(within(row).getByRole('link')).toHaveAttribute('href', '/runs/r1')
})
