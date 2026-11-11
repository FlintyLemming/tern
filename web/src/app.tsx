import { QueryCache, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useEffect, type ReactNode } from 'react'
import { BrowserRouter, Route, Routes, useNavigate } from 'react-router'
import { ApiError, configureClient } from './api/client'
import { forgetSession, qk } from './api/queries'
import { LoginPage } from './pages/login/LoginPage'
import { SetupPage } from './pages/setup/SetupPage'
import { UnlockPage } from './pages/unlock/UnlockPage'
import { AppShell } from './shell/AppShell'
import { AuthGate } from './shell/AuthGate'
import { Toaster } from './ui/Toast'
import './i18n'

const queryClient: QueryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // a 4xx will not change by asking again; network trouble and 5xx may
      retry: (n, e) => n < 2 && !(e instanceof ApiError && e.status >= 400 && e.status < 500),
      refetchOnWindowFocus: true,
    },
  },
  queryCache: new QueryCache({
    onError: (e) => {
      // the vault got locked under us: let the gate send us to /unlock
      if (e instanceof ApiError && (e.code === 'vault_locked' || e.code === 'vault_unavailable')) {
        void queryClient.invalidateQueries({ queryKey: qk.status })
      }
    },
  }),
})

function ClientWiring() {
  const navigate = useNavigate()
  useEffect(() => {
    configureClient({ navigate: (to) => navigate(to, { replace: true }), onUnauthorized: () => forgetSession(queryClient) })
  }, [navigate])
  return null
}

// Pages are filled in by later tasks.
function Todo({ children }: { children: ReactNode }) {
  return <h1 className="text-page">{children}</h1>
}

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <Toaster>
        <BrowserRouter>
          <ClientWiring />
          <Routes>
            <Route element={<AuthGate />}>
              <Route path="/setup" element={<SetupPage />} />
              <Route path="/login" element={<LoginPage />} />
              <Route path="/unlock" element={<UnlockPage />} />
              <Route element={<AppShell />}>
                <Route index element={<Todo>overview</Todo>} />
                <Route path="/tasks" element={<Todo>tasks</Todo>} />
                <Route path="/tasks/new" element={<Todo>new task</Todo>} />
                <Route path="/tasks/:id" element={<Todo>task</Todo>} />
                <Route path="/runs" element={<Todo>runs</Todo>} />
                <Route path="/runs/:id" element={<Todo>run</Todo>} />
                <Route path="/connections" element={<Todo>connections</Todo>} />
                <Route path="/snapshots" element={<Todo>snapshots</Todo>} />
                <Route path="/settings" element={<Todo>settings</Todo>} />
                <Route path="*" element={<Todo>404</Todo>} />
              </Route>
            </Route>
          </Routes>
        </BrowserRouter>
      </Toaster>
    </QueryClientProvider>
  )
}
