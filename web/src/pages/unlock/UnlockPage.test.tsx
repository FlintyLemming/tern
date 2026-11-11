import { screen, waitFor } from '@testing-library/react'
import { Route } from 'react-router'
import { afterEach } from 'vitest'
import { fakeFetch } from '../../test/fetch'
import { renderRoutes } from '../../test/render'
import { UnlockPage } from './UnlockPage'

afterEach(() => vi.unstubAllGlobals())

it('unlock: wrong passphrase message', async () => {
  fakeFetch({ 'POST /api/v1/unlock': { status: 403, body: { error: { code: 'forbidden', message: 'wrong passphrase' } } } })
  const { user } = renderRoutes(<Route path="/unlock" element={<UnlockPage />} />, '/unlock')
  await user.type(screen.getByLabelText('口令'), 'not it at all')
  await user.click(screen.getByRole('button', { name: '解锁' }))
  expect(await screen.findByText('口令不对')).toBeInTheDocument()
  expect(screen.getByLabelText('口令')).toHaveValue('')
})

it('unlock: success marks the vault unlocked', async () => {
  const f = fakeFetch({ 'POST /api/v1/unlock': { status: 204 } })
  const { user, qc } = renderRoutes(<Route path="/unlock" element={<UnlockPage />} />, '/unlock')
  qc.setQueryData(['status'], { version: 'dev', setup_required: false, vault_state: 'locked', authenticated: true })
  await user.type(screen.getByLabelText('口令'), 'the real passphrase')
  await user.click(screen.getByRole('button', { name: '解锁' }))
  await waitFor(() => expect(qc.getQueryData(['status'])).toMatchObject({ vault_state: 'unlocked' }))
  expect(f.calls[0].body).toEqual({ passphrase: 'the real passphrase' })
  expect(screen.getByLabelText('口令')).toHaveValue('')
})
