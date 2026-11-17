import { screen, waitFor, within } from '@testing-library/react'
import { Route } from 'react-router'
import { afterEach } from 'vitest'
import { fakeFetch, type Reply } from '../../test/fetch'
import { system } from '../../test/data'
import { inMutationCache, Loc, renderRoutes } from '../../test/render'
import { SettingsPage } from './SettingsPage'

afterEach(() => vi.unstubAllGlobals())

const settings = { node_name: 'nas-home', max_concurrent: 1, log_keep_days: 90, log_keep_runs: 200 }

function renderSettings(routes: Record<string, Reply | Reply[]> = {}, sys = system) {
  const f = fakeFetch({ 'GET /api/v1/system': { body: sys }, 'GET /api/v1/settings': { body: settings }, ...routes })
  const r = renderRoutes(
    <>
      <Route path="/settings" element={<SettingsPage />} />
      <Route path="*" element={<Loc />} />
    </>,
    '/settings',
  )
  return { f, ...r }
}

it('shows node, zone and version', async () => {
  renderSettings()
  await waitFor(() => expect(screen.getByLabelText('节点名')).toHaveValue('nas-home'))
  expect(screen.getByText('Asia/Shanghai')).toBeInTheDocument()
  expect(screen.getByText('dev')).toBeInTheDocument()
})

it('validates concurrency range', async () => {
  const { f, user } = renderSettings({ 'PUT /api/v1/settings': { body: settings } })
  const conc = await screen.findByLabelText('全局同时运行的任务数')
  await waitFor(() => expect(conc).toHaveValue('1'))
  for (const bad of ['0', '9']) {
    await user.clear(conc)
    await user.type(conc, bad)
    await user.click(screen.getByRole('button', { name: '保存设置' }))
    expect(screen.getByText('要在 1 到 8 之间')).toBeInTheDocument()
  }
  expect(f.of('PUT', '/api/v1/settings')).toHaveLength(0)
  await user.clear(conc)
  await user.type(conc, '3')
  await user.click(screen.getByRole('button', { name: '保存设置' }))
  await waitFor(() => expect(f.of('PUT', '/api/v1/settings')).toHaveLength(1))
  expect(f.of('PUT', '/api/v1/settings')[0].body).toEqual({ node_name: 'nas-home', max_concurrent: 3, log_keep_days: 90, log_keep_runs: 200 })
})

it('change password redirects to login', async () => {
  const { f, user, qc } = renderSettings({ 'POST /api/v1/settings/password': { status: 204 } })
  const card = (await screen.findByRole('heading', { name: '修改管理员密码' })).closest('section')!
  await user.type(within(card).getByLabelText('当前密码'), 'old password 1')
  await user.type(within(card).getByLabelText('新密码'), 'new password 22')
  await user.type(within(card).getByLabelText('再输一遍新密码'), 'new password 22')
  await user.click(within(card).getByRole('button', { name: '修改密码' }))
  expect(await screen.findByTestId('loc')).toHaveTextContent('/login')
  expect(f.of('POST', '/api/v1/settings/password')[0].body).toEqual({ current_password: 'old password 1', new_password: 'new password 22' })
  await waitFor(() => expect(inMutationCache(qc, 'new password 22')).toBe(false))
  expect(inMutationCache(qc, 'old password 1')).toBe(false)
})

it('change password checks the new one locally', async () => {
  const { f, user } = renderSettings()
  const card = (await screen.findByRole('heading', { name: '修改管理员密码' })).closest('section')!
  await user.type(within(card).getByLabelText('当前密码'), 'old password 1')
  await user.type(within(card).getByLabelText('新密码'), 'short')
  await user.type(within(card).getByLabelText('再输一遍新密码'), 'short')
  await user.click(within(card).getByRole('button', { name: '修改密码' }))
  expect(within(card).getByText('至少 10 个字符')).toBeInTheDocument()
  expect(f.of('POST', '/api/v1/settings/password')).toHaveLength(0)
})

it('has no unlock mode to choose: the admin password is the only one', async () => {
  renderSettings()
  expect(await screen.findByRole('heading', { name: '修改管理员密码' })).toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: '解锁方式' })).toBeNull()
  expect(screen.queryByText(/口令/)).toBeNull()
})

it('missing tool flagged', async () => {
  renderSettings({}, { ...system, tools: [{ name: 'restic', version: '0.19.1', found: true }, { name: 'rclone', version: '', found: false }] })
  const missing = (await screen.findByText('rclone')).closest('tr')!
  expect(within(missing).getByText('没找到')).toHaveClass('text-danger')
  const ok = screen.getByText('restic').closest('tr')!
  expect(within(ok).queryByText('没找到')).toBeNull()
  expect(within(ok).getByText('0.19.1')).toBeInTheDocument()
})

it('language switch persists', async () => {
  const { user } = renderSettings()
  await user.click(await screen.findByRole('radio', { name: 'English' }))
  expect(localStorage.getItem('tern.lang')).toBe('en')
  expect(await screen.findByRole('heading', { name: 'Settings' })).toBeInTheDocument()
})

it('explains a zone the browser cannot name', async () => {
  renderSettings({}, { ...system, timezone: 'Local' })
  expect(await screen.findByText('容器没有设置 TZ 环境变量，界面上的时间按 UTC 显示。')).toBeInTheDocument()
})
