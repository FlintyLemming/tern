import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { MemoryRouter, Routes, useLocation } from 'react-router'
import { Toaster } from '../ui/Toast'

// Loc shows where the router is, for tests that follow navigation.
export function Loc() {
  const l = useLocation()
  return <p data-testid="loc">{l.pathname + l.search}</p>
}

export function newQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity }, mutations: { retry: false } } })
}

// renderRoutes renders <Route> elements inside every provider the app has.
export function renderRoutes(routes: ReactNode, path = '/', opts: { qc?: QueryClient; user?: ReturnType<typeof userEvent.setup> } = {}) {
  const qc = opts.qc ?? newQueryClient()
  const user = opts.user ?? userEvent.setup()
  const utils = render(
    <QueryClientProvider client={qc}>
      <Toaster>
        <MemoryRouter initialEntries={[path]}>
          <Routes>{routes}</Routes>
        </MemoryRouter>
      </Toaster>
    </QueryClientProvider>,
  )
  return { qc, user, ...utils }
}
