import { screen } from '@testing-library/react'
import { Route } from 'react-router'
import { afterEach } from 'vitest'
import { fakeFetch } from '../test/fetch'
import { system } from '../test/data'
import { renderRoutes } from '../test/render'
import { NodeTime } from './NodeTime'

afterEach(() => vi.unstubAllGlobals())

it('shows nothing in the browser zone while the node zone is unknown', async () => {
  let release!: () => void
  const gate = new Promise<void>((r) => (release = r))
  fakeFetch({
    'GET /api/v1/system': async () => {
      await gate
      return { body: { ...system, timezone: 'America/New_York' } }
    },
  })
  renderRoutes(<Route path="/" element={<NodeTime iso="2026-11-10T19:00:00Z" />} />)
  expect(screen.getByText('…')).toBeInTheDocument()
  release()
  expect(await screen.findByText('2026-11-10 14:00')).toBeInTheDocument()
})
