import { screen, waitFor, within } from '@testing-library/react'
import { Route } from 'react-router'
import { afterEach } from 'vitest'
import type { Task } from '../../api/types'
import { fakeFetch, type Reply } from '../../test/fetch'
import { backupTask, davConn, fiveTimes, overview, sshConn, syncTask, system } from '../../test/data'
import { Loc, renderRoutes } from '../../test/render'
import { choose } from '../../test/select'
import { TaskEditor } from './TaskEditor'
import { TasksPage } from './TasksPage'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function renderAt(path: string, routes: Record<string, Reply | Reply[]> = {}) {
  const f = fakeFetch({
    'GET /api/v1/system': { body: system },
    'GET /api/v1/connections': { body: [sshConn, davConn] },
    'GET /api/v1/overview': { body: overview },
    'GET /api/v1/tasks': { body: [backupTask, syncTask] },
    'GET /api/v1/tasks/t1': { body: backupTask },
    'GET /api/v1/tasks/t2': { body: syncTask },
    'POST /api/v1/schedule/preview': { body: { next: fiveTimes, period_sec: 86400 } },
    'POST /api/v1/tasks/repo-password': { body: { password: 'GENERATEDPASS0123456789abcdefghi' } },
    ...routes,
  })
  const r = renderRoutes(
    <>
      <Route path="/tasks" element={<TasksPage />} />
      <Route path="/tasks/new" element={<TaskEditor />} />
      <Route path="/tasks/:id" element={<TaskEditor />} />
      <Route path="*" element={<Loc />} />
    </>,
    path,
  )
  return { f, ...r }
}

const created = (t: Task, extra: Record<string, unknown> = {}) => ({ status: 201, body: { ...t, ...extra } })

async function fillNewSync(user: ReturnType<typeof renderAt>['user'], protocol = 'SFTP') {
  await user.type(await screen.findByLabelText('名称'), 'music')
  await choose(user, '协议', protocol)
  await choose(user, '连接', protocol === 'WebDAV' ? 'dav' : 'nas2')
  const pair = screen.getAllByTestId('pair')[0]
  await user.type(within(pair).getByLabelText('本机路径'), '/data/music')
  await user.type(within(pair).getByLabelText('远端路径'), '/srv/music')
}

const lastBody = (f: ReturnType<typeof renderAt>['f'], method: string, path: string) => {
  const calls = f.of(method, path)
  // eslint-free escape hatch: the bodies are checked field by field below
  return calls[calls.length - 1].body as Record<string, any>
}

// ---- sync

it('sync: rsync hides concurrency and submits 0', async () => {
  const { f, user } = renderAt('/tasks/new?type=sync', { 'POST /api/v1/tasks': created(syncTask) })
  await fillNewSync(user)
  expect(screen.getByLabelText('并发文件数')).toBeInTheDocument()
  await choose(user, '协议', 'rsync over SSH')
  expect(screen.queryByLabelText('并发文件数')).toBeNull()
  await user.click(screen.getByRole('button', { name: '保存' }))
  await waitFor(() => expect(f.of('POST', '/api/v1/tasks')).toHaveLength(1))
  const body = lastBody(f, 'POST', '/api/v1/tasks')
  expect(body.type).toBe('sync')
  expect(body.sync.protocol).toBe('rsync')
  expect(body.sync.transfers).toBe(0)
  expect(body.sync.pairs).toEqual([{ local: '/data/music', remote: '/srv/music' }])
  expect(body.backup).toBeUndefined()
  expect('repo_password' in body).toBe(false)
  expect('recovery_confirmed' in body).toBe(false)
})

it('sync: protocol hint text is exact', async () => {
  renderAt('/tasks/new?type=sync')
  expect(await screen.findByText('大文件多、网络差，优先选 rsync。')).toBeInTheDocument()
})

it('sync: connection list filtered by protocol', async () => {
  const { user } = renderAt('/tasks/new?type=sync')
  await screen.findByLabelText('名称')
  await choose(user, '协议', 'WebDAV')
  await user.click(screen.getByLabelText('连接'))
  expect(await screen.findByRole('option', { name: 'dav' })).toBeInTheDocument()
  expect(screen.queryByRole('option', { name: 'nas2' })).toBeNull()
})

it('sync: mirror shows warning and delete limit', async () => {
  const { user } = renderAt('/tasks/new?type=sync')
  await screen.findByLabelText('名称')
  expect(screen.queryByText('目的地多出来的文件会被删除。')).toBeNull()
  await choose(user, '删除策略', '镜像')
  expect(screen.getByText('目的地多出来的文件会被删除。')).toBeInTheDocument()
  expect(screen.getByLabelText('单次最多删除')).toHaveValue('100')
  await choose(user, '删除策略', '只增改，不删除')
  expect(screen.queryByText('目的地多出来的文件会被删除。')).toBeNull()
  expect(screen.queryByLabelText('单次最多删除')).toBeNull()
})

it('sync: folder pairs can be added and removed', async () => {
  const { user } = renderAt('/tasks/t2')
  await waitFor(() => expect(screen.getAllByTestId('pair')).toHaveLength(2))
  await user.click(screen.getByRole('button', { name: '添加文件夹对' }))
  expect(screen.getAllByTestId('pair')).toHaveLength(3)
  await user.click(within(screen.getAllByTestId('pair')[0]).getByRole('button', { name: '移除' }))
  const pairs = screen.getAllByTestId('pair')
  expect(pairs).toHaveLength(2)
  expect(within(pairs[0]).getByLabelText('本机路径')).toHaveValue('/data/videos')
})

// ---- schedule

it('schedule: preview shows five times in node zone', async () => {
  const { f } = renderAt('/tasks/new?type=sync')
  const list = await screen.findByRole('list', { name: /未来 5 次运行/ })
  await waitFor(() => expect(within(list).getAllByRole('listitem')).toHaveLength(5))
  expect(within(list).getByText('2026-11-12 03:00')).toBeInTheDocument() // 19:00Z in Asia/Shanghai
  expect(screen.getByText(/Asia\/Shanghai/)).toBeInTheDocument()
  expect(f.of('POST', '/api/v1/schedule/preview')[0].body).toEqual({ schedule: '0 3 * * *' })
})

it('schedule: invalid expression shows error, save blocked', async () => {
  const { f, user } = renderAt('/tasks/new?type=sync', {
    'POST /api/v1/schedule/preview': (call) =>
      (call.body as { schedule: string }).schedule === '61 * * * *'
        ? { status: 400, body: { error: { code: 'validation', message: 'invalid schedule', fields: { schedule: 'invalid' } } } }
        : { body: { next: fiveTimes, period_sec: 86400 } },
  })
  await fillNewSync(user)
  const cron = screen.getByLabelText('cron 表达式')
  await user.clear(cron)
  await user.type(cron, '61 * * * *')
  await waitFor(() => expect(cron).toHaveAttribute('aria-invalid', 'true'))
  expect(screen.getByText('格式不对')).toBeInTheDocument()
  const save = screen.getByRole('button', { name: '保存' })
  expect(save).toBeDisabled()
  await user.click(save)
  expect(f.of('POST', '/api/v1/tasks')).toHaveLength(0)
})

it('schedule: presets fill the expression; manual has no preview', async () => {
  const { user } = renderAt('/tasks/new?type=sync')
  await screen.findByLabelText('名称')
  await choose(user, '计划', '每小时')
  expect(screen.getByLabelText('cron 表达式')).toHaveValue('0 * * * *')
  await choose(user, '计划', '只手动')
  expect(screen.getByLabelText('cron 表达式')).toHaveValue('')
  expect(screen.queryByRole('list', { name: /未来 5 次运行/ })).toBeNull()
})

// ---- hooks

it('hooks: ignore-failure only on pre', async () => {
  renderAt('/tasks/new?type=sync')
  const pre = await screen.findByRole('group', { name: '任务前命令' })
  const post = screen.getByRole('group', { name: '任务后命令' })
  expect(within(pre).getByLabelText('失败时忽略并继续')).toBeInTheDocument()
  expect(within(post).queryByLabelText('失败时忽略并继续')).toBeNull()
})

it('hooks: shows the code execution warning', async () => {
  renderAt('/tasks/new?type=backup')
  expect(await screen.findByText('能登录这个界面的人，可以通过这里在容器里执行任意命令。')).toBeInTheDocument()
})

// ---- backup

async function fillNewBackup(user: ReturnType<typeof renderAt>['user']) {
  await user.type(await screen.findByLabelText('名称'), 'mail')
  await user.type(within(screen.getAllByTestId('source')[0]).getByLabelText('源目录'), '/data/mail')
  await choose(user, '仓库连接', 'nas2')
  await user.type(screen.getByLabelText('远端路径'), '/srv/mail-repo')
  await waitFor(() => expect(screen.getByLabelText('仓库密码')).toHaveValue('GENERATEDPASS0123456789abcdefghi'))
}

it('backup: save opens recovery dialog, no request yet', async () => {
  const { f, user } = renderAt('/tasks/new?type=backup')
  await fillNewBackup(user)
  await user.click(screen.getByRole('button', { name: '保存' }))
  const dlg = await screen.findByRole('dialog')
  expect(within(dlg).getByText('sftp:backup@nas2.example:/srv/mail-repo')).toBeInTheDocument()
  expect(within(dlg).getByText('GENERATEDPASS0123456789abcdefghi')).toBeInTheDocument()
  expect(f.of('POST', '/api/v1/tasks')).toHaveLength(0)
})

it('backup: create disabled until confirmed', async () => {
  const { f, user } = renderAt('/tasks/new?type=backup', { 'POST /api/v1/tasks': created(backupTask, { repo_state: 'new' }) })
  await fillNewBackup(user)
  await user.click(screen.getByRole('button', { name: '保存' }))
  const dlg = await screen.findByRole('dialog')
  const create = within(dlg).getByRole('button', { name: '确认并创建' })
  expect(create).toBeDisabled()
  await user.click(within(dlg).getByLabelText('我已把恢复信息保存到这台机器以外的地方'))
  expect(create).toBeEnabled()
  await user.click(create)
  await waitFor(() => expect(f.of('POST', '/api/v1/tasks')).toHaveLength(1))
  const body = lastBody(f, 'POST', '/api/v1/tasks')
  expect(body.recovery_confirmed).toBe(true)
  expect(body.repo_password).toBe('GENERATEDPASS0123456789abcdefghi')
  expect(body.backup.sources).toEqual(['/data/mail'])
  expect(body.backup.repo_path).toBe('/srv/mail-repo')
  expect(body.sync).toBeUndefined()
  expect(await screen.findByText('已初始化新仓库')).toBeInTheDocument()
})

it('backup: own password is used as typed', async () => {
  const { f, user } = renderAt('/tasks/new?type=backup', { 'POST /api/v1/tasks': created(backupTask, { repo_state: 'existing' }) })
  await fillNewBackup(user)
  await user.click(screen.getByRole('radio', { name: '自己填写' }))
  const pw = screen.getByLabelText('仓库密码')
  expect(pw).toHaveValue('')
  expect(pw).toHaveAttribute('type', 'password')
  await user.type(pw, 'my own repo pw')
  await user.click(screen.getByRole('button', { name: '保存' }))
  const dlg = await screen.findByRole('dialog')
  await user.click(within(dlg).getByLabelText('我已把恢复信息保存到这台机器以外的地方'))
  await user.click(within(dlg).getByRole('button', { name: '确认并创建' }))
  await waitFor(() => expect(f.of('POST', '/api/v1/tasks')).toHaveLength(1))
  expect(lastBody(f, 'POST', '/api/v1/tasks').repo_password).toBe('my own repo pw')
  expect(await screen.findByText('已接入现有仓库')).toBeInTheDocument()
})

it('backup: download contains repository and password', async () => {
  const blobs: Blob[] = []
  vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: (b: Blob) => (blobs.push(b), 'blob:x'), revokeObjectURL: () => {} }))
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  const { user } = renderAt('/tasks/new?type=backup')
  await fillNewBackup(user)
  await user.click(screen.getByRole('button', { name: '保存' }))
  const dlg = await screen.findByRole('dialog')
  await user.click(within(dlg).getByRole('button', { name: '下载恢复信息' }))
  expect(blobs).toHaveLength(1)
  const text = await blobs[0].text()
  expect(text).toContain('sftp:backup@nas2.example:/srv/mail-repo')
  expect(text).toContain('GENERATEDPASS0123456789abcdefghi')
})

it('backup: wrong repo password shown at field', async () => {
  const { user } = renderAt('/tasks/new?type=backup', {
    'POST /api/v1/tasks': { status: 400, body: { error: { code: 'wrong_repo_password', message: 'the repository exists and this is not its password' } } },
  })
  await fillNewBackup(user)
  await user.click(screen.getByRole('button', { name: '保存' }))
  const dlg = await screen.findByRole('dialog')
  await user.click(within(dlg).getByLabelText('我已把恢复信息保存到这台机器以外的地方'))
  await user.click(within(dlg).getByRole('button', { name: '确认并创建' }))
  const pw = await screen.findByLabelText('仓库密码')
  await waitFor(() => expect(pw).toHaveAttribute('aria-invalid', 'true'))
  const err = document.getElementById(pw.getAttribute('aria-describedby')!.split(' ')[0])
  expect(err).toHaveTextContent('这个路径下已有仓库，密码不对。')
  expect(screen.queryByRole('dialog')).toBeNull()
})

it('backup: no repo password field when editing', async () => {
  const { f } = renderAt('/tasks/t1')
  await waitFor(() => expect(screen.getByLabelText('名称')).toHaveValue('docs'))
  expect(screen.queryByLabelText('仓库密码')).toBeNull()
  expect(f.of('POST', '/api/v1/tasks/repo-password')).toHaveLength(0)
})

it('backup: editing saves without the recovery dialog', async () => {
  const { f, user } = renderAt('/tasks/t1', { 'PUT /api/v1/tasks/t1': { body: backupTask } })
  await waitFor(() => expect(screen.getByLabelText('名称')).toHaveValue('docs'))
  await user.click(screen.getByRole('button', { name: '保存' }))
  await waitFor(() => expect(f.of('PUT', '/api/v1/tasks/t1')).toHaveLength(1))
  const body = lastBody(f, 'PUT', '/api/v1/tasks/t1')
  expect(body).toEqual(backupTask) // exactly the task, nothing the server would refuse as unknown
})

// ---- errors and races

it('shows server field errors next to inputs', async () => {
  const { user } = renderAt('/tasks/t2', {
    'PUT /api/v1/tasks/t2': { status: 400, body: { error: { code: 'validation', message: 'invalid task', fields: { 'sync.pairs[1].local': 'overlaps' } } } },
  })
  await waitFor(() => expect(screen.getAllByTestId('pair')).toHaveLength(2))
  await user.click(screen.getByRole('button', { name: '保存' }))
  const second = screen.getAllByTestId('pair')[1]
  expect(await within(second).findByText('和另一组文件夹对的路径重叠')).toBeInTheDocument()
  expect(within(second).getByLabelText('本机路径')).toHaveAttribute('aria-invalid', 'true')
  expect(within(screen.getAllByTestId('pair')[0]).getByLabelText('本机路径')).not.toHaveAttribute('aria-invalid', 'true')
})

it('409 conflict shows reload banner, keeps form', async () => {
  const { f, user } = renderAt('/tasks/t2', {
    'PUT /api/v1/tasks/t2': { status: 409, body: { error: { code: 'conflict', message: 'the task was changed by someone else; reload and retry' } } },
  })
  const name = await screen.findByLabelText('名称')
  await waitFor(() => expect(name).toHaveValue('photos'))
  await user.clear(name)
  await user.type(name, 'photos-renamed')
  await user.click(screen.getByRole('button', { name: '保存' }))
  expect(await screen.findByText('这个任务已在别处被修改。')).toBeInTheDocument()
  expect(screen.getByLabelText('名称')).toHaveValue('photos-renamed')
  expect(lastBody(f, 'PUT', '/api/v1/tasks/t2').version).toBe(4)
  // reloading takes the other edit and drops ours
  f.set('GET /api/v1/tasks/t2', { body: { ...syncTask, version: 5, name: 'photos-theirs' } })
  await user.click(screen.getByRole('button', { name: '重新加载' }))
  await waitFor(() => expect(screen.getByLabelText('名称')).toHaveValue('photos-theirs'))
  expect(screen.queryByText('这个任务已在别处被修改。')).toBeNull()
})

it('double click save sends one request', async () => {
  let release!: () => void
  const gate = new Promise<void>((r) => (release = r))
  const { f, user } = renderAt('/tasks/new?type=sync', {
    'POST /api/v1/tasks': async () => {
      await gate
      return created(syncTask)
    },
  })
  await fillNewSync(user)
  const save = screen.getByRole('button', { name: '保存' })
  await user.dblClick(save)
  await user.click(save)
  expect(f.of('POST', '/api/v1/tasks')).toHaveLength(1)
  release()
  expect(await screen.findByRole('button', { name: '新建任务' })).toBeInTheDocument() // back on the list
  expect(f.of('POST', '/api/v1/tasks')).toHaveLength(1)
})

// ---- list

it('dry run only offered for sync tasks', async () => {
  const { f, user } = renderAt('/tasks', { 'POST /api/v1/tasks/t2/run': { status: 202, body: { run_id: 'r9' } } })
  const backupRow = (await screen.findByRole('link', { name: 'docs' })).closest('tr')!
  const syncRow = screen.getByRole('link', { name: 'photos' }).closest('tr')!
  expect(within(backupRow).queryByRole('button', { name: '试运行' })).toBeNull()
  await user.click(within(syncRow).getByRole('button', { name: '试运行' }))
  await waitFor(() => expect(f.of('POST', '/api/v1/tasks/t2/run')).toHaveLength(1))
  expect(lastBody(f, 'POST', '/api/v1/tasks/t2/run')).toEqual({ kind: 'dry_run' })
})

it('list shows next run and last result from the overview', async () => {
  renderAt('/tasks')
  const row = (await screen.findByRole('link', { name: 'photos' })).closest('tr')!
  expect(await within(row).findByText('失败')).toBeInTheDocument()
  expect(within(row).getByText('2026-11-12 03:00')).toBeInTheDocument()
})

it('list: enable switch and run now', async () => {
  const { f, user } = renderAt('/tasks', {
    'POST /api/v1/tasks/t1/enabled': { body: { ...backupTask, enabled: false } },
    'POST /api/v1/tasks/t1/run': { status: 202, body: { run_id: 'r1' } },
  })
  const row = (await screen.findByRole('link', { name: 'docs' })).closest('tr')!
  await user.click(within(row).getByRole('switch'))
  await waitFor(() => expect(f.of('POST', '/api/v1/tasks/t1/enabled')).toHaveLength(1))
  expect(lastBody(f, 'POST', '/api/v1/tasks/t1/enabled')).toEqual({ enabled: false })
  await user.click(within(row).getByRole('button', { name: '立即运行' }))
  await waitFor(() => expect(f.of('POST', '/api/v1/tasks/t1/run')).toHaveLength(1))
  expect(lastBody(f, 'POST', '/api/v1/tasks/t1/run')).toEqual({})
})

it('list: command line dialog lists each line', async () => {
  const { user } = renderAt('/tasks', {
    'GET /api/v1/tasks/t2/command-line': { body: { lines: ['rclone sync /data/photos remote:/srv/photos', 'rclone sync /data/videos remote:/srv/videos'] } },
  })
  const row = (await screen.findByRole('link', { name: 'photos' })).closest('tr')!
  await user.click(within(row).getByRole('button', { name: '等价命令行' }))
  const dlg = await screen.findByRole('dialog')
  expect(await within(dlg).findByText('rclone sync /data/photos remote:/srv/photos')).toBeInTheDocument()
  expect(within(dlg).getAllByRole('button', { name: '复制' })).toHaveLength(2)
})

// ---- folder pickers

const folders = (path: string, names: string[]) => ({
  body: { path, missing: false, truncated: false, entries: names.map((n) => ({ name: n, path: `${path === '/' ? '' : path}/${n}`, mount: false })) },
})
const browseButton = (input: HTMLElement) => within(input.parentElement!).getByRole('button', { name: '浏览文件夹' })
const askedPath = (url: string) => new URL(url, 'http://x').searchParams.get('path') ?? ''

it('backup: sources browse this machine, the repository path its connection', async () => {
  const { f, user } = renderAt('/tasks/new?type=backup', {
    'GET /api/v1/browse': folders('/data', ['mail']),
    'GET /api/v1/connections/c1/browse': folders('/srv', ['repos']),
  })
  const source = within((await screen.findAllByTestId('source'))[0]).getByLabelText('源目录')
  await user.click(browseButton(source))
  let picker = await screen.findByRole('dialog', { name: '选择文件夹' })
  await within(picker).findByRole('button', { name: 'mail' })
  await user.click(within(picker).getByRole('button', { name: '选择此文件夹' }))
  expect(source).toHaveValue('/data')

  const repo = screen.getByLabelText('远端路径')
  expect(browseButton(repo)).toBeDisabled()
  await choose(user, '仓库连接', 'nas2')
  await user.click(browseButton(repo))
  picker = await screen.findByRole('dialog', { name: '选择文件夹' })
  expect(within(picker).getByText('nas2 上的文件夹')).toBeInTheDocument()
  await within(picker).findByRole('button', { name: 'repos' })
  await user.click(within(picker).getByRole('button', { name: '选择此文件夹' }))
  expect(repo).toHaveValue('/srv')
  expect(f.of('GET', '/api/v1/connections/c1/browse').map((c) => askedPath(c.url))).toEqual([''])
})

it('sync: each side of a folder pair browses its own end', async () => {
  const { f, user } = renderAt('/tasks/new?type=sync', {
    'GET /api/v1/browse': folders('/data', ['music']),
    'GET /api/v1/connections/c2/browse': folders('/', ['Music']),
  })
  await screen.findByLabelText('名称')
  const pair = screen.getAllByTestId('pair')[0]
  const remote = within(pair).getByLabelText('远端路径')
  expect(browseButton(remote)).toBeDisabled()
  await choose(user, '协议', 'WebDAV')
  await choose(user, '连接', 'dav')
  await user.click(browseButton(remote))
  let picker = await screen.findByRole('dialog', { name: '选择文件夹' })
  await user.click(await within(picker).findByRole('button', { name: 'Music' }))
  await waitFor(() => expect(f.of('GET', '/api/v1/connections/c2/browse')).toHaveLength(2))
  expect(f.of('GET', '/api/v1/connections/c2/browse').map((c) => askedPath(c.url))).toEqual(['/', '/Music'])

  await user.click(within(picker).getByRole('button', { name: '取消' }))
  const local = within(pair).getByLabelText('本机路径')
  await user.click(browseButton(local))
  picker = await screen.findByRole('dialog', { name: '选择文件夹' })
  await within(picker).findByRole('button', { name: 'music' })
  await user.click(within(picker).getByRole('button', { name: '选择此文件夹' }))
  expect(local).toHaveValue('/data')
})
