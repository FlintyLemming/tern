import { act, screen, waitFor, within } from '@testing-library/react'
import { Route } from 'react-router'
import { afterEach } from 'vitest'
import { connectEvents } from '../../api/events'
import type { Overview, OverviewItem, SystemInfo } from '../../api/types'
import { fakeFetch } from '../../test/fetch'
import { FakeES } from '../../test/eventsource'
import { overview as baseOverview, system } from '../../test/data'
import { renderRoutes } from '../../test/render'
import { OverviewPage } from './OverviewPage'

afterEach(() => vi.unstubAllGlobals())

const item = (over: Partial<OverviewItem>): OverviewItem => ({ ...baseOverview.tasks[0], ...over })

function renderOverview(ov: Overview, sys: SystemInfo = system) {
  const f = fakeFetch({ 'GET /api/v1/system': { body: sys }, 'GET /api/v1/overview': { body: ov } })
  const r = renderRoutes(<Route path="/" element={<OverviewPage />} />, '/')
  return { f, ...r }
}

it('stale banner lists stale tasks', async () => {
  renderOverview({
    tasks: [
      item({ task_id: 'a', name: 'docs', stale: true, last_success_at: '2026-11-01T19:00:00Z' }),
      item({ task_id: 'b', name: 'photos', type: 'sync', stale: true, last_success_at: null }),
      item({ task_id: 'c', name: 'fine', stale: false }),
    ],
  })
  const banner = await screen.findByRole('alert')
  expect(banner).toHaveTextContent('2 个任务超过预期时间没有成功')
  expect(within(banner).getByRole('link', { name: 'docs' })).toHaveAttribute('href', '/tasks/a')
  expect(within(banner).getByRole('link', { name: 'photos' })).toHaveAttribute('href', '/tasks/b')
  expect(within(banner).queryByText('fine')).toBeNull()
  expect(await within(banner).findByText(/2026-11-02 03:00/)).toBeInTheDocument()
  expect(within(banner).getByText(/从未/)).toBeInTheDocument()
})

it('no banner when nothing is stale', async () => {
  renderOverview({ tasks: [item({ stale: false })] })
  await screen.findByRole('link', { name: 'docs' })
  expect(screen.queryByRole('alert')).toBeNull()
})

it('check failure banner', async () => {
  renderOverview({ tasks: [item({ task_id: 't1', name: 'docs', last_check_status: 'failed' })] })
  const banner = await screen.findByRole('alert')
  expect(banner).toHaveTextContent('仓库校验失败：docs')
  expect(within(banner).getByRole('link')).toHaveAttribute('href', '/runs?task=t1')
})

it('last success shows never', async () => {
  renderOverview({ tasks: [item({ name: 'photos', last_success_at: null, last_status: 'failed' })] })
  const row = (await screen.findByRole('link', { name: 'photos' })).closest('tr')!
  expect(within(row).getByText('从未')).toBeInTheDocument()
})

it('last success is relative with the node-zone time on hover', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-11-11T12:00:00Z') })
  try {
    renderOverview({ tasks: [item({ last_success_at: '2026-11-11T09:00:00Z' })] })
    const row = (await screen.findByRole('link', { name: 'docs' })).closest('tr')!
    const cell = await within(row).findByText('3 小时前')
    await waitFor(() => expect(cell.closest('[title]')).toHaveAttribute('title', '2026-11-11 17:00 Asia/Shanghai'))
  } finally {
    vi.useRealTimers()
  }
})

const running = (progress: OverviewItem['active_run']) => ({ tasks: [item({ name: 'docs', active_run: progress })] })

it('progress row for active run', async () => {
  renderOverview(
    running({ run_id: 'r7', status: 'running', progress: { unit: '/data', bytes_done: 1048576, bytes_total: 4194304, files_done: 1, files_total: 4, speed_bps: 2097152, eta_sec: 90 } }),
  )
  expect(await screen.findByText('已传 1 MiB / 4 MiB · 2 MiB/s · 预计剩余 1:30')).toBeInTheDocument()
  expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '1048576')
})

it('progress updates from run.progress event', async () => {
  const { f, qc } = renderOverview(
    running({ run_id: 'r7', status: 'running', progress: { unit: '/data', bytes_done: 1048576, bytes_total: 4194304, files_done: 1, files_total: 4, speed_bps: 2097152, eta_sec: 90 } }),
  )
  await screen.findByText(/已传 1 MiB/)
  FakeES.all = []
  const stop = connectEvents(qc, FakeES.make)
  const before = f.of('GET', '/api/v1/overview').length
  act(() =>
    FakeES.all[0].emit('run.progress', {
      run_id: 'r7',
      progress: { unit: '/data', bytes_done: 3145728, bytes_total: 4194304, files_done: 3, files_total: 4, speed_bps: 1048576, eta_sec: 1 },
    }),
  )
  expect(await screen.findByText('已传 3 MiB / 4 MiB · 1 MiB/s · 预计剩余 0:01')).toBeInTheDocument()
  expect(f.of('GET', '/api/v1/overview').length).toBe(before)
  stop()
})

it('retry_wait shows waiting text', async () => {
  renderOverview(running({ run_id: 'r7', status: 'retry_wait', progress: null }))
  expect(await screen.findByText('等待重试')).toBeInTheDocument()
  expect(screen.queryByText(/已传/)).toBeNull()
})

const bbrRows: [string, SystemInfo['bbr'], string[]][] = [
  ['active', { available: ['reno', 'bbr'], current: 'bbr', supported: true, active: true }, ['BBR 已生效']],
  [
    'supported',
    { available: ['reno', 'cubic', 'bbr'], current: 'cubic', supported: true, active: false },
    ['内核支持 BBR，但当前算法是 cubic', 'net.ipv4.tcp_congestion_control: bbr', '然后重建容器'],
  ],
  ['missing', { available: ['reno', 'cubic'], current: 'cubic', supported: false, active: false }, ['宿主机内核没有加载 tcp_bbr 模块', 'modprobe tcp_bbr', '后重建容器']],
]

it.each(bbrRows)('BBR card: three states (%s)', async (_n, bbr, texts) => {
  renderOverview({ tasks: [] }, { ...system, bbr })
  const card = (await screen.findByRole('heading', { name: 'BBR' })).closest('section')!
  for (const s of texts) expect(await within(card).findByText((_c, el) => el?.tagName !== 'SECTION' && (el?.textContent ?? '').includes(s) && el!.children.length === 0)).toBeInTheDocument()
  expect(within(card).getByText('BBR 只对本机发出的数据起作用，也就是推送和备份。拉取要看对端自己的设置。')).toBeInTheDocument()
})

it('run now from the overview', async () => {
  const { f, user } = renderOverview({ tasks: [item({ task_id: 't1', name: 'docs' })] })
  f.set('POST /api/v1/tasks/t1/run', { status: 202, body: { run_id: 'r9' } })
  const row = (await screen.findByRole('link', { name: 'docs' })).closest('tr')!
  await user.click(within(row).getByRole('button', { name: '立即运行' }))
  await waitFor(() => expect(f.of('POST', '/api/v1/tasks/t1/run')).toHaveLength(1))
})
