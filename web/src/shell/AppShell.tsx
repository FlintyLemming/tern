import * as RD from '@radix-ui/react-dialog'
import { useQueryClient } from '@tanstack/react-query'
import { Activity, FolderSync, HardDrive, History, KeyRound, LayoutDashboard, LogOut, Menu, Settings as SettingsIcon, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router'
import { request } from '../api/client'
import { connectEvents } from '../api/events'
import { forgetSession } from '../api/queries'
import { cx } from '../lib/cx'
import { useNodeZone } from './NodeTime'

const NAV = [
  { to: '/', key: 'nav.overview', icon: LayoutDashboard, end: true },
  { to: '/tasks', key: 'nav.tasks', icon: FolderSync, end: false },
  { to: '/runs', key: 'nav.runs', icon: History, end: false },
  { to: '/connections', key: 'nav.connections', icon: KeyRound, end: false },
  { to: '/snapshots', key: 'nav.snapshots', icon: HardDrive, end: false },
  { to: '/settings', key: 'nav.settings', icon: SettingsIcon, end: false },
] as const

function NavRows({ onNavigate }: { onNavigate?: () => void }) {
  const { t } = useTranslation()
  return (
    <nav className="flex flex-col gap-0.5">
      {NAV.map(({ to, key, icon: Icon, end }) => (
        <NavLink
          key={to}
          to={to}
          end={end}
          onClick={onNavigate}
          className={({ isActive }) =>
            cx(
              'flex items-center gap-3 rounded-sm px-4 py-3 text-body',
              isActive ? 'bg-canvas-soft font-semibold text-primary' : 'text-ink-secondary hover:bg-canvas-soft',
            )
          }
        >
          <Icon aria-hidden className="size-4" />
          {t(key)}
        </NavLink>
      ))}
    </nav>
  )
}

function SidebarFoot() {
  const { t } = useTranslation()
  const zone = useNodeZone()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const logout = async () => {
    try {
      await request('POST', '/api/v1/logout')
    } finally {
      forgetSession(qc)
      navigate('/login')
    }
  }
  return (
    <div className="mt-auto flex flex-col gap-2 px-4 pt-4 text-caption text-ink-muted">
      {zone ? <p>{t('shell.zone', { zone })}</p> : null}
      <button type="button" onClick={() => void logout()} className="inline-flex items-center gap-2 self-start text-ink-secondary hover:text-ink">
        <LogOut aria-hidden className="size-4" />
        {t('shell.logout')}
      </button>
    </div>
  )
}

function Brand() {
  return (
    <div className="flex items-center gap-2 px-4 py-3 text-title">
      <Activity aria-hidden className="size-5 text-ink" />
      tern
    </div>
  )
}

// AppShell: a 240px sidebar beside a centred 1080px column; at 600px and
// below the sidebar becomes a top bar with a drawer.
export function AppShell() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const loc = useLocation()

  useEffect(() => connectEvents(qc), [qc])
  useEffect(() => setOpen(false), [loc.pathname])

  return (
    <div className="flex min-h-screen bg-canvas-soft max-[600px]:flex-col">
      <aside className="sticky top-0 flex h-screen w-60 shrink-0 flex-col border-r border-hairline bg-surface py-3 max-[600px]:hidden">
        <Brand />
        <div className="px-2">
          <NavRows />
        </div>
        <div className="pb-2">
          <SidebarFoot />
        </div>
      </aside>

      <header className="sticky top-0 z-30 hidden items-center justify-between border-b border-hairline bg-surface px-2 max-[600px]:flex">
        <Brand />
        <RD.Root open={open} onOpenChange={setOpen}>
          <RD.Trigger aria-label={t('shell.menu')} className="rounded-md p-3 text-ink-secondary">
            <Menu aria-hidden className="size-5" />
          </RD.Trigger>
          <RD.Portal>
            <RD.Overlay className="fixed inset-0 z-40 bg-ink/20" />
            <RD.Content aria-describedby={undefined} className="fixed inset-y-0 left-0 z-50 flex w-72 max-w-[85vw] flex-col bg-surface py-3 shadow-2 focus:outline-none">
              <div className="flex items-center justify-between pr-2">
                <RD.Title asChild>
                  <div>
                    <Brand />
                  </div>
                </RD.Title>
                <RD.Close aria-label={t('common.close')} className="rounded-md p-3 text-ink-muted">
                  <X aria-hidden className="size-5" />
                </RD.Close>
              </div>
              <div className="px-2">
                <NavRows onNavigate={() => setOpen(false)} />
              </div>
              <SidebarFoot />
            </RD.Content>
          </RD.Portal>
        </RD.Root>
      </header>

      <main className="mx-auto w-full max-w-[1080px] min-w-0 px-6 py-8 max-[600px]:px-4 max-[600px]:py-6">
        <Outlet />
      </main>
    </div>
  )
}
