import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach } from 'vitest'
import { AuthGate } from './AuthGate'
import { fakeFetch } from '../test/fetch'

afterEach(() => vi.unstubAllGlobals())

function Where({ name }: { name: string }) {
  const loc = useLocation()
  return (
    <p>
      {name} {loc.pathname + loc.search}
    </p>
  )
}

function renderAt(path: string, status: Record<string, unknown>) {
  const f = fakeFetch({
    'GET /api/v1/status': { body: { version: 'dev', ...status } },
    'GET /api/v1/session': { body: { csrf_token: 'c' } },
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route element={<AuthGate />}>
            <Route path="/setup" element={<Where name="SETUP" />} />
            <Route path="/login" element={<Where name="LOGIN" />} />
            <Route path="/unlock" element={<Where name="UNLOCK" />} />
            <Route path="*" element={<Where name="APP" />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
  return f
}

const rows: [string, Record<string, unknown>, RegExp][] = [
  ['keyfile missing', { setup_required: false, vault_state: 'keyfile_missing', authenticated: true }, /密钥文件/],
  ['keyfile mismatch', { setup_required: false, vault_state: 'keyfile_mismatch', authenticated: false }, /密钥文件/],
  ['setup required', { setup_required: true, vault_state: 'uninitialized', authenticated: false }, /^SETUP \/setup$/],
  ['not logged in', { setup_required: false, vault_state: 'unlocked', authenticated: false }, /^LOGIN \/login\?next=%2Ftasks%2Fx$/],
  ['locked', { setup_required: false, vault_state: 'locked', authenticated: true }, /^UNLOCK \/unlock\?next=%2Ftasks%2Fx$/],
  ['unlocked', { setup_required: false, vault_state: 'unlocked', authenticated: true }, /^APP \/tasks\/x$/],
]

it.each(rows)('routes by status: %s', async (name, status, want) => {
  renderAt('/tasks/x', status)
  if (name.startsWith('keyfile')) expect(await screen.findByRole('heading', { name: want })).toBeInTheDocument()
  else expect(await screen.findByText(want)).toBeInTheDocument()
})

it('keyfile error page has no form', async () => {
  renderAt('/', { setup_required: false, vault_state: 'keyfile_missing', authenticated: true })
  await screen.findByRole('heading', { name: /密钥文件/ })
  expect(document.querySelector('input')).toBeNull()
  expect(document.querySelector('button[type=submit]')).toBeNull()
  expect(document.querySelector('form')).toBeNull()
})

it('fetches the CSRF token before showing the app', async () => {
  const f = renderAt('/', { setup_required: false, vault_state: 'unlocked', authenticated: true })
  await screen.findByText(/^APP/)
  expect(f.of('GET', '/api/v1/session')).toHaveLength(1)
})

it('leaves the login page for next once logged in', async () => {
  renderAt('/login?next=%2Fruns', { setup_required: false, vault_state: 'unlocked', authenticated: true })
  expect(await screen.findByText(/^APP \/runs$/)).toBeInTheDocument()
})

it('ignores a next that leaves the site', async () => {
  renderAt('/login?next=https%3A%2F%2Fevil.example', { setup_required: false, vault_state: 'unlocked', authenticated: true })
  expect(await screen.findByText(/^APP \/$/)).toBeInTheDocument()
})
