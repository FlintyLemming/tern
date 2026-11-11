import { useQuery } from '@tanstack/react-query'
import { Navigate, Outlet, useLocation } from 'react-router'
import { useTranslation } from 'react-i18next'
import { setCsrf } from '../api/client'
import { errorText } from '../api/errors'
import { fetchSession, qk, useStatus } from '../api/queries'
import type { AppStatus } from '../api/types'
import { safeNext, withNext } from '../lib/nav'
import { Button } from '../ui/Button'
import { Card } from '../ui/Card'

const PUBLIC = ['/setup', '/login', '/unlock']

type Decision = { kind: 'keyfile'; mismatch: boolean } | { kind: 'redirect'; to: string } | { kind: 'outlet' }

// decide applies the routing table of the plan, top row first.
export function decide(s: AppStatus, path: string, search: string): Decision {
  const onPublic = PUBLIC.includes(path)
  const next = safeNext(new URLSearchParams(search).get('next'))
  const back = onPublic ? next : path + search
  if (s.vault_state === 'keyfile_missing' || s.vault_state === 'keyfile_mismatch') {
    return { kind: 'keyfile', mismatch: s.vault_state === 'keyfile_mismatch' }
  }
  if (s.setup_required) return path === '/setup' ? { kind: 'outlet' } : { kind: 'redirect', to: '/setup' }
  if (!s.authenticated) return path === '/login' ? { kind: 'outlet' } : { kind: 'redirect', to: withNext('/login', back) }
  if (s.vault_state === 'locked') return path === '/unlock' ? { kind: 'outlet' } : { kind: 'redirect', to: withNext('/unlock', back) }
  if (onPublic) return { kind: 'redirect', to: next ?? '/' }
  return { kind: 'outlet' }
}

export function FullScreen({ children }: { children: React.ReactNode }) {
  return <div className="flex min-h-screen items-center justify-center bg-canvas-soft p-4">{children}</div>
}

function KeyfilePage({ mismatch }: { mismatch: boolean }) {
  const { t } = useTranslation()
  return (
    <FullScreen>
      <div className="w-full max-w-lg rounded-xl border border-danger bg-surface p-6">
        <h1 className="mb-3 text-title text-danger">{t(mismatch ? 'keyfile.title_mismatch' : 'keyfile.title_missing')}</h1>
        <p className="mb-3">{t(mismatch ? 'keyfile.body_mismatch' : 'keyfile.body_missing')}</p>
        <p className="mb-3 font-semibold">{t('keyfile.fix')}</p>
        <p className="text-caption text-ink-muted">{t('keyfile.note')}</p>
      </div>
    </FullScreen>
  )
}

export function Loading() {
  const { t } = useTranslation()
  return (
    <FullScreen>
      <p className="text-caption text-ink-muted">{t('common.loading')}</p>
    </FullScreen>
  )
}

// AuthGate decides from GET /status where the visitor belongs and renders the
// matching route, or the key file error page.
export function AuthGate() {
  const { t } = useTranslation()
  const loc = useLocation()
  const status = useStatus()
  const s = status.data
  const wantSession = Boolean(s && s.authenticated && !s.setup_required && s.vault_state !== 'keyfile_missing' && s.vault_state !== 'keyfile_mismatch')
  const session = useQuery({
    queryKey: qk.session,
    queryFn: async () => {
      const r = await fetchSession()
      setCsrf(r.csrf_token)
      return r
    },
    enabled: wantSession,
    staleTime: Infinity,
  })

  const failed = (error: unknown, retry: () => void) => (
    <FullScreen>
      <Card title={t('shell.status_failed')}>
        <p className="mb-4 text-caption text-ink-muted">{errorText(t, error)}</p>
        <Button onClick={retry}>{t('common.retry')}</Button>
      </Card>
    </FullScreen>
  )

  if (status.isPending) return <Loading />
  if (status.isError || !s) return failed(status.error, () => void status.refetch())
  const d = decide(s, loc.pathname, loc.search)
  if (d.kind === 'keyfile') return <KeyfilePage mismatch={d.mismatch} />
  if (d.kind === 'redirect') return <Navigate to={d.to} replace />
  if (wantSession && !session.data) {
    // a 401 here has already sent us to the login page (see client.ts)
    return session.isError ? failed(session.error, () => void session.refetch()) : <Loading />
  }
  return <Outlet />
}
