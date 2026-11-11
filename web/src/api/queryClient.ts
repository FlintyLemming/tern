import { QueryCache, QueryClient, type DefaultOptions } from '@tanstack/react-query'
import { ApiError } from './client'
import { qk } from './queries'

// createQueryClient is the one QueryClient setup, for the app and its tests.
//
// Mutations are dropped from memory as soon as nothing shows them
// (gcTime 0): their variables and results include passwords, private keys
// and repository passwords, which must not outlive the form that sent them.
export function createQueryClient(overrides: DefaultOptions = {}): QueryClient {
  const qc: QueryClient = new QueryClient({
    defaultOptions: {
      queries: {
        // a 4xx will not change by asking again; network trouble and 5xx may
        retry: (n, e) => n < 2 && !(e instanceof ApiError && e.status >= 400 && e.status < 500),
        refetchOnWindowFocus: true,
        ...overrides.queries,
      },
      mutations: { ...overrides.mutations, gcTime: 0 },
    },
    queryCache: new QueryCache({
      onError: (e) => {
        // the vault got locked under us: let the gate send us to /unlock
        if (e instanceof ApiError && (e.code === 'vault_locked' || e.code === 'vault_unavailable')) {
          void qc.invalidateQueries({ queryKey: qk.status })
        }
      },
    }),
  })
  return qc
}
