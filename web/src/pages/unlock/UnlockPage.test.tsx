import { screen, waitFor } from '@testing-library/react'
import { Route } from 'react-router'
import { afterEach } from 'vitest'
import { fakeFetch } from '../../test/fetch'
import { renderRoutes } from '../../test/render'
import { UnlockPage } from './UnlockPage'

afterEach(() => vi.unstubAllGlobals())

const locked = (secrets = true) => ({
  body: { version: 'dev', setup_required: false, vault_state: 'locked', authenticated: true, mounts: { data: true, secrets } },
})

it('unlock: says the passphrase is needed this once', async () => {
  fakeFetch({ 'GET /api/v1/status': locked() })
  renderRoutes(<Route path="/unlock" element={<UnlockPage />} />, '/unlock')
  expect(await screen.findByText(/输入一次以后，tern 会改为自动解锁/)).toBeInTheDocument()
  expect(screen.queryByText(/\/secrets 没有挂载/)).toBeNull()
})

it('unlock: warns that it comes back without /secrets', async () => {
  fakeFetch({ 'GET /api/v1/status': locked(false) })
  renderRoutes(<Route path="/unlock" element={<UnlockPage />} />, '/unlock')
  expect(await screen.findByText(/\/secrets 没有挂载，所以每次重启后还要输入一次/)).toBeInTheDocument()
})

it('unlock: wrong passphrase message', async () => {
  fakeFetch({ 'GET /api/v1/status': locked(), 'POST /api/v1/unlock': { status: 403, body: { error: { code: 'forbidden', message: 'wrong passphrase' } } } })
  const { user } = renderRoutes(<Route path="/unlock" element={<UnlockPage />} />, '/unlock')
  await user.type(screen.getByLabelText('解锁口令'), 'not it at all')
  await user.click(screen.getByRole('button', { name: '解锁' }))
  expect(await screen.findByText('口令不对')).toBeInTheDocument()
  expect(screen.getByLabelText('解锁口令')).toHaveValue('')
})

it('unlock: success marks the vault unlocked', async () => {
  // What the server says before and, once the unlock went out, after.
  const status = () => {
    const r = locked()
    if (f.of('POST', '/api/v1/unlock').length > 0) r.body.vault_state = 'unlocked'
    return r
  }
  const f = fakeFetch({ 'GET /api/v1/status': status, 'POST /api/v1/unlock': { status: 204 } })
  const { user, qc } = renderRoutes(<Route path="/unlock" element={<UnlockPage />} />, '/unlock')
  await user.type(screen.getByLabelText('解锁口令'), 'the real passphrase')
  await user.click(screen.getByRole('button', { name: '解锁' }))
  await waitFor(() => expect(qc.getQueryData(['status'])).toMatchObject({ vault_state: 'unlocked' }))
  expect(f.of('POST', '/api/v1/unlock')[0].body).toEqual({ passphrase: 'the real passphrase' })
  expect(screen.getByLabelText('解锁口令')).toHaveValue('')
})
