import { screen } from '@testing-library/react'
import { Route } from 'react-router'
import { afterEach } from 'vitest'
import { fakeFetch } from '../test/fetch'
import { FakeES } from '../test/eventsource'
import { renderRoutes } from '../test/render'
import { LoginPage } from '../pages/login/LoginPage'
import { AppShell } from './AppShell'
import { AuthGate } from './AuthGate'

afterEach(() => vi.unstubAllGlobals())

const status = { version: 'dev', setup_required: false, vault_state: 'unlocked', authenticated: true }

function renderShell(extra: Record<string, unknown>) {
  vi.stubGlobal('EventSource', FakeES)
  fakeFetch({ 'GET /api/v1/status': { body: { ...status, ...extra } } })
  renderRoutes(
    <Route element={<AppShell />}>
      <Route index element={<p>home</p>} />
    </Route>,
    '/',
  )
}

it('signed in with the password: log out button', async () => {
  renderShell({ platform: 'docker', signed_in_via: 'password' })
  expect((await screen.findAllByRole('button', { name: '退出登录' })).length).toBeGreaterThan(0)
})

it('signed in through QTS: who, and no log out', async () => {
  renderShell({ platform: 'qts', signed_in_via: 'qts', qts_user: 'alice' })
  expect((await screen.findAllByText('已通过 QTS 登录：alice')).length).toBeGreaterThan(0)
  expect(screen.queryByRole('button', { name: '退出登录' })).toBeNull()
})

// Setup or an old login may leave a password session next to the QTS one;
// ending it must not strand the window on the login page.
it('on QTS, logging out of the password session falls back to the QTS sign-in', async () => {
  vi.stubGlobal('EventSource', FakeES)
  const f = fakeFetch({
    'GET /api/v1/status': [
      { body: { ...status, platform: 'qts', signed_in_via: 'password' } },
      { body: { ...status, platform: 'qts', signed_in_via: 'qts', qts_user: 'alice' } },
    ],
    'GET /api/v1/session': { body: { csrf_token: 'c' } },
    'POST /api/v1/logout': { status: 204 },
  })
  const { user } = renderRoutes(
    <Route element={<AuthGate />}>
      <Route path="/login" element={<LoginPage />} />
      <Route element={<AppShell />}>
        <Route index element={<p>home</p>} />
      </Route>
    </Route>,
    '/',
  )
  await user.click((await screen.findAllByRole('button', { name: '退出登录' }))[0])
  expect((await screen.findAllByText('已通过 QTS 登录：alice')).length).toBeGreaterThan(0)
  expect(screen.getAllByText('home').length).toBeGreaterThan(0)
  expect(screen.queryByLabelText('管理员密码')).toBeNull()
  expect(f.calls.some((c) => c.method === 'POST' && c.path === '/api/v1/logout')).toBe(true)
})
