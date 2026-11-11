import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { createQueryClient } from '../api/queryClient'
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
  return createQueryClient({ queries: { retry: false, gcTime: Infinity }, mutations: { retry: false } })
}

// renderRoutes renders <Route> elements inside every provider the app has.
export function renderRoutes(routes: ReactNode, path = '/', opts: { qc?: QueryClient; user?: ReturnType<typeof userEvent.setup> } = {}) {
  const qc = opts.qc ?? newQueryClient()
  const user = opts.user ?? userEvent.setup({ delay: null }) // no timer between keystrokes
  const utils = render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <Toaster>
          <Routes>{routes}</Routes>
        </Toaster>
      </MemoryRouter>
    </QueryClientProvider>,
  )
  return { qc, user, ...utils }
}

// inMutationCache reports whether any mutation the client still holds
// (variables, result or error) contains the text.
export function inMutationCache(qc: QueryClient, text: string): boolean {
  return qc
    .getMutationCache()
    .getAll()
    .some((m) => JSON.stringify(m.state).includes(text))
}
