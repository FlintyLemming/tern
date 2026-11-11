import { screen, waitFor, within } from '@testing-library/react'
import { Route } from 'react-router'
import { afterEach } from 'vitest'
import type { Snapshot, SnapshotList } from '../../api/types'
import { fakeFetch, type Reply } from '../../test/fetch'
import { backupTask, fiveTimes, sshConn, syncTask, system } from '../../test/data'
import { Loc, renderRoutes } from '../../test/render'
import { TaskEditor } from '../tasks/TaskEditor'
import { SnapshotsPage } from './SnapshotsPage'

afterEach(() => vi.unstubAllGlobals())

const snap = (over: Partial<Snapshot>): Snapshot => ({
  id: 'abcd1234',
  short_id: 'abcd1234',
  time: '2026-11-10T19:00:00Z',
  host: 'nas-home',
  paths: ['/data/docs'],
  tags: ['tern-t1'],
  mine: true,
  ...over,
})

const list = (snaps: Snapshot[], total = snaps.length): SnapshotList => ({ total, snapshots: snaps, repository: 'sftp:backup@nas2.example:/srv/repo' })

function renderAt(path: string, routes: Record<string, Reply | Reply[]> = {}) {
  const f = fakeFetch({
    'GET /api/v1/system': { body: system },
    'GET /api/v1/tasks': { body: [backupTask, syncTask] },
    'GET /api/v1/tasks/t1': { body: backupTask },
    'GET /api/v1/connections': { body: [sshConn] },
    'POST /api/v1/schedule/preview': { body: { next: fiveTimes, period_sec: 86400 } },
    'GET /api/v1/tasks/t1/snapshots': { body: list([snap({})]) },
    ...routes,
  })
  const r = renderRoutes(
    <>
      <Route path="/snapshots" element={<SnapshotsPage />} />
      <Route path="/tasks/:id" element={<TaskEditor />} />
      <Route path="*" element={<Loc />} />
    </>,
    path,
  )
  return { f, ...r }
}

it('lists snapshots newest first with mine badge', async () => {
  renderAt('/snapshots?task=t1', {
    'GET /api/v1/tasks/t1/snapshots': {
      body: list([
        snap({ id: 'old1', short_id: 'old1', time: '2026-11-01T19:00:00Z' }),
        snap({ id: 'new1', short_id: 'new1', time: '2026-11-10T19:00:00Z' }),
        snap({ id: 'oth1', short_id: 'oth1', time: '2026-11-05T19:00:00Z', mine: false, host: 'laptop', tags: null }),
      ]),
    },
  })
  await screen.findByText('new1')
  const rows = screen.getAllByRole('row').slice(1)
  expect(rows.map((r) => within(r).getAllByRole('cell')[1].textContent)).toEqual(['new1', 'oth1', 'old1'])
  expect(within(rows[0]).getByText('本任务')).toBeInTheDocument()
  expect(within(rows[1]).queryByText('本任务')).toBeNull()
  expect(within(rows[1]).getByText('laptop')).toBeInTheDocument()
  expect(await within(rows[0]).findByText('2026-11-11 03:00')).toBeInTheDocument()
})

it('only backup tasks can be picked', async () => {
  const { user } = renderAt('/snapshots')
  await user.click(await screen.findByLabelText('备份任务'))
  expect(await screen.findByRole('option', { name: 'docs' })).toBeInTheDocument()
  expect(screen.queryByRole('option', { name: 'photos' })).toBeNull()
})

it('shows total note when capped', async () => {
  const snaps = Array.from({ length: 200 }, (_, i) => snap({ id: `s${i}`, short_id: `s${i}` }))
  renderAt('/snapshots?task=t1', { 'GET /api/v1/tasks/t1/snapshots': { body: list(snaps, 3000) } })
  expect(await screen.findByText(/共 3000 个/)).toBeInTheDocument()
  expect(screen.getByText('只显示最新 200 个，共 3000 个')).toBeInTheDocument()
})

async function openRestore(r: ReturnType<typeof renderAt>) {
  const row = (await screen.findByText('abcd1234')).closest('tr')!
  await r.user.click(within(row).getByRole('button', { name: '恢复' }))
  return screen.findByRole('dialog')
}

it('restore: command preview updates live', async () => {
  const r = renderAt('/snapshots?task=t1')
  const dlg = await openRestore(r)
  expect(within(dlg).getByLabelText('目标目录')).toHaveAttribute('placeholder', '/targets/restore-abcd1234')
  await r.user.type(within(dlg).getByLabelText('子路径（可选）'), '/photos/2024')
  await r.user.type(within(dlg).getByLabelText('目标目录'), '/targets/r1')
  expect(within(dlg).getByText('restic -r sftp:backup@nas2.example:/srv/repo restore abcd1234:/photos/2024 --target /targets/r1')).toBeInTheDocument()
  await r.user.clear(within(dlg).getByLabelText('子路径（可选）'))
  expect(within(dlg).getByText('restic -r sftp:backup@nas2.example:/srv/repo restore abcd1234 --target /targets/r1')).toBeInTheDocument()
})

it('restore: target required', async () => {
  const r = renderAt('/snapshots?task=t1')
  const dlg = await openRestore(r)
  await r.user.click(within(dlg).getByRole('button', { name: '开始恢复' }))
  expect(within(dlg).getByText('必填')).toBeInTheDocument()
  expect(r.f.of('POST', '/api/v1/tasks/t1/restore')).toHaveLength(0)
})

const codes: [string, string][] = [
  ['not_absolute', '要填容器内的绝对路径'],
  ['not_empty', '目标目录已有内容。换一个新目录'],
  ['not_mounted', '这个目录不在挂载卷里，恢复出的数据会随容器消失'],
  ['not_writable', '这个目录不可写。检查挂载是不是只读'],
  ['inside_source', '不能恢复到备份的源目录里'],
  ['inside_data', '不能恢复到本工具自己的数据目录里'],
]

it.each(codes)('restore: maps target_dir error codes (%s)', async (code, text) => {
  const r = renderAt('/snapshots?task=t1', {
    'POST /api/v1/tasks/t1/restore': { status: 400, body: { error: { code: 'validation', message: 'bad target', fields: { target_dir: code } } } },
  })
  const dlg = await openRestore(r)
  await r.user.type(within(dlg).getByLabelText('目标目录'), '/targets/x')
  await r.user.click(within(dlg).getByRole('button', { name: '开始恢复' }))
  expect(await within(dlg).findByText(text)).toBeInTheDocument()
  expect(within(dlg).getByLabelText('目标目录')).toHaveAttribute('aria-invalid', 'true')
})

it('restore: navigates to the run', async () => {
  const r = renderAt('/snapshots?task=t1', { 'POST /api/v1/tasks/t1/restore': { status: 202, body: { run_id: 'r42' } } })
  const dlg = await openRestore(r)
  await r.user.type(within(dlg).getByLabelText('目标目录'), '/targets/r1')
  await r.user.click(within(dlg).getByRole('button', { name: '开始恢复' }))
  expect(await screen.findByTestId('loc')).toHaveTextContent('/runs/r42')
  expect(r.f.of('POST', '/api/v1/tasks/t1/restore')[0].body).toEqual({ snapshot_id: 'abcd1234', target_dir: '/targets/r1' })
})

async function openRecovery(r: ReturnType<typeof renderAt>) {
  await waitFor(() => expect(screen.getByLabelText('名称')).toHaveValue('docs'))
  await r.user.click(screen.getByRole('button', { name: '查看恢复信息' }))
  return screen.findByRole('dialog')
}

const info = { repository: 'sftp:backup@nas2.example:/srv/repo', password: 'REPO-SECRET-PW', commands: ["export RESTIC_REPOSITORY='sftp:backup@nas2.example:/srv/repo'", 'restic snapshots'] }

it('recovery info: requires admin password', async () => {
  const r = renderAt('/tasks/t1', { 'POST /api/v1/tasks/t1/recovery-info': [{ status: 403, body: { error: { code: 'forbidden', message: 'wrong current password' } } }, { body: info }] })
  const dlg = await openRecovery(r)
  expect(document.body.textContent).not.toContain('REPO-SECRET-PW')
  const pw = within(dlg).getByLabelText('管理员密码')
  expect(pw).toHaveAttribute('type', 'password')
  await r.user.type(pw, 'wrong one here')
  await r.user.click(within(dlg).getByRole('button', { name: '查看' }))
  expect(await within(dlg).findByText('密码不对')).toBeInTheDocument()
  await r.user.type(within(dlg).getByLabelText('管理员密码'), 'right one here')
  await r.user.click(within(dlg).getByRole('button', { name: '查看' }))
  expect(await within(dlg).findByText('REPO-SECRET-PW')).toBeInTheDocument()
  expect(r.f.of('POST', '/api/v1/tasks/t1/recovery-info')[1].body).toEqual({ password: 'right one here' })
  expect(within(dlg).getByText("export RESTIC_REPOSITORY='sftp:backup@nas2.example:/srv/repo'")).toBeInTheDocument()
})

it('recovery info: cleared on close', async () => {
  const r = renderAt('/tasks/t1', { 'POST /api/v1/tasks/t1/recovery-info': { body: info } })
  let dlg = await openRecovery(r)
  await r.user.type(within(dlg).getByLabelText('管理员密码'), 'right one here')
  await r.user.click(within(dlg).getByRole('button', { name: '查看' }))
  await within(dlg).findByText('REPO-SECRET-PW')
  await r.user.click(within(dlg).getByRole('button', { name: '关闭' }))
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  expect(document.body.textContent).not.toContain('REPO-SECRET-PW')
  dlg = await openRecovery(r)
  expect(within(dlg).getByLabelText('管理员密码')).toHaveValue('')
  expect(document.body.textContent).not.toContain('REPO-SECRET-PW')
})
