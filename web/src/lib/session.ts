import type { QueryClient } from '@tanstack/react-query'
import { qk } from '../api/queries'
import type { AppStatus } from '../api/types'

// markStatus updates what the gate knows at once (so it routes without waiting)
// and asks the server for the truth behind it.
export function markStatus(qc: QueryClient, patch: Partial<AppStatus>) {
  qc.setQueryData<AppStatus>(qk.status, (s) => ({ version: '', setup_required: false, vault_state: 'unlocked', authenticated: true, ...s, ...patch }))
  void qc.invalidateQueries({ queryKey: qk.status })
}
