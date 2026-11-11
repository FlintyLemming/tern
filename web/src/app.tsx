import { QueryClientProvider } from '@tanstack/react-query'
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { BrowserRouter, Link, Route, Routes, useNavigate } from 'react-router'
import { configureClient } from './api/client'
import { forgetSession } from './api/queries'
import { createQueryClient } from './api/queryClient'
import { ConnectionsPage } from './pages/connections/ConnectionsPage'
import { LoginPage } from './pages/login/LoginPage'
import { TaskEditor } from './pages/tasks/TaskEditor'
import { TasksPage } from './pages/tasks/TasksPage'
import { OverviewPage } from './pages/overview/OverviewPage'
import { RunDetailPage } from './pages/runs/RunDetailPage'
import { RunsPage } from './pages/runs/RunsPage'
import { SettingsPage } from './pages/settings/SettingsPage'
import { SnapshotsPage } from './pages/snapshots/SnapshotsPage'
import { SetupPage } from './pages/setup/SetupPage'
import { UnlockPage } from './pages/unlock/UnlockPage'
import { AppShell } from './shell/AppShell'
import { AuthGate } from './shell/AuthGate'
import { EmptyState } from './ui/EmptyState'
import { Toaster } from './ui/Toast'
import './i18n'

const queryClient = createQueryClient()

function ClientWiring() {
  const navigate = useNavigate()
  useEffect(() => {
    configureClient({ navigate: (to) => navigate(to, { replace: true }), onUnauthorized: () => forgetSession(queryClient) })
  }, [navigate])
  return null
}

function NotFound() {
  const { t } = useTranslation()
  return (
    <EmptyState
      action={
        <Link to="/" className="text-primary underline">
          {t('common.back_home')}
        </Link>
      }
    >
      {t('common.not_found_page')}
    </EmptyState>
  )
}

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <Toaster>
          <ClientWiring />
          <Routes>
            <Route element={<AuthGate />}>
              <Route path="/setup" element={<SetupPage />} />
              <Route path="/login" element={<LoginPage />} />
              <Route path="/unlock" element={<UnlockPage />} />
              <Route element={<AppShell />}>
                <Route index element={<OverviewPage />} />
                <Route path="/tasks" element={<TasksPage />} />
                <Route path="/tasks/new" element={<TaskEditor />} />
                <Route path="/tasks/:id" element={<TaskEditor />} />
                <Route path="/runs" element={<RunsPage />} />
                <Route path="/runs/:id" element={<RunDetailPage />} />
                <Route path="/connections" element={<ConnectionsPage />} />
                <Route path="/snapshots" element={<SnapshotsPage />} />
                <Route path="/settings" element={<SettingsPage />} />
                <Route path="*" element={<NotFound />} />
              </Route>
            </Route>
          </Routes>
        </Toaster>
      </BrowserRouter>
    </QueryClientProvider>
  )
}
