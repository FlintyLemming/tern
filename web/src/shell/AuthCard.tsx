import type { ReactNode } from 'react'
import { FullScreen } from './AuthGate'

// The card of the setup, login and unlock pages: centred, 16px radius, 24px padding.
export function AuthCard({ title, children }: { title: ReactNode; children: ReactNode }) {
  return (
    <FullScreen>
      <div className="w-full max-w-md rounded-xl border border-hairline bg-surface p-6 shadow-1">
        <h1 className="mb-6 text-page">{title}</h1>
        {children}
      </div>
    </FullScreen>
  )
}
