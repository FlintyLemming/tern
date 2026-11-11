import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Route } from 'react-router'
import { afterEach } from 'vitest'
import { AuthGate } from '../../shell/AuthGate'
import { fakeFetch } from '../../test/fetch'
import { inMutationCache, Loc, renderRoutes } from '../../test/render'
import { LoginPage } from './LoginPage'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

const out = { body: { version: 'dev', setup_required: false, vault_state: 'unlocked', authenticated: false } }
const inside = { body: { version: 'dev', setup_required: false, vault_state: 'unlocked', authenticated: true } }

function renderLogin(path: string, login: Parameters<typeof fakeFetch>[0][string], user?: ReturnType<typeof userEvent.setup>) {
  const f = fakeFetch({
    'GET /api/v1/status': [out, inside],
    'GET /api/v1/session': { body: { csrf_token: 'c' } },
    'POST /api/v1/login': login,
  })
  const r = renderRoutes(
    <Route element={<AuthGate />}>
      <Route path="/login" element={<LoginPage />} />
      <Route path="*" element={<Loc />} />
    </Route>,
    path,
    { user },
  )
  return { f, ...r }
}

it('login: wrong password message', async () => {
  const { user } = renderLogin('/login', { status: 401, body: { error: { code: 'unauthorized', message: 'wrong password' } } })
  await user.type(await screen.findByLabelText('管理员密码'), 'nope nope nope')
  await user.click(screen.getByRole('button', { name: '登录' }))
  expect(await screen.findByText('密码不对')).toBeInTheDocument()
  expect(screen.getByLabelText('管理员密码')).toHaveValue('')
})

it('login: rate limit countdown', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime.bind(vi) })
  renderLogin('/login', { status: 429, body: { error: { code: 'rate_limited', message: 'slow down' } }, headers: { 'Retry-After': '60' } }, user)
  await user.type(await screen.findByLabelText('管理员密码'), 'whatever whatever')
  await user.click(screen.getByRole('button', { name: '登录' }))
  expect(await screen.findByText(/60 秒/)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: '登录' })).toBeDisabled()
  await act(async () => vi.advanceTimersByTime(30_000))
  expect(screen.getByText(/30 秒/)).toBeInTheDocument()
  await act(async () => vi.advanceTimersByTime(30_000))
  expect(screen.queryByText(/秒后再试/)).toBeNull()
  expect(screen.getByRole('button', { name: '登录' })).toBeEnabled()
})

it('login: honors next only for same-origin paths', async () => {
  const ok = { body: { csrf_token: 'c' } }
  const a = renderLogin('/login?next=%2Ftasks', ok)
  await a.user.type(await screen.findByLabelText('管理员密码'), 'correct horse battery')
  await a.user.click(screen.getByRole('button', { name: '登录' }))
  expect(await screen.findByText('/tasks')).toBeInTheDocument()
  expect(a.f.of('POST', '/api/v1/login')[0].body).toEqual({ password: 'correct horse battery' })
  a.unmount()
  vi.unstubAllGlobals()

  const b = renderLogin('/login?next=https%3A%2F%2Fevil.example', ok)
  await b.user.type(await screen.findByLabelText('管理员密码'), 'correct horse battery')
  await b.user.click(screen.getByRole('button', { name: '登录' }))
  await waitFor(() => expect(screen.getByTestId('loc')).toHaveTextContent(/^\/$/))
})

it('login: a password does not linger in memory', async () => {
  const { user, qc } = renderLogin('/login', { status: 401, body: { error: { code: 'unauthorized', message: 'wrong password' } } })
  await user.type(await screen.findByLabelText('管理员密码'), 'almost-the-password')
  await user.click(screen.getByRole('button', { name: '登录' }))
  await screen.findByText('密码不对')
  await waitFor(() => expect(inMutationCache(qc, 'almost-the-password')).toBe(false))
})
