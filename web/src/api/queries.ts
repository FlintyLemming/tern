import { useInfiniteQuery, useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { applyPlatform } from '../i18n'
import { request } from './client'
import type {
  AppStatus,
  Connection,
  ConnectionInput,
  CreatedTask,
  CsrfToken,
  Overview,
  ProbeResult,
  Progress,
  Recovery,
  Run,
  RunKind,
  RunStarted,
  SchedulePreview,
  Settings,
  SnapshotList,
  SSHKey,
  SystemInfo,
  Task,
  TestResult,
} from './types'

export interface RunFilter {
  task_id?: string
}

// Query keys. Prefixes matter: invalidating ['tasks'] also hits every task.
export const qk = {
  status: ['status'] as const,
  session: ['session'] as const,
  system: ['system'] as const,
  settings: ['settings'] as const,
  keys: ['ssh-keys'] as const,
  connections: ['connections'] as const,
  tasks: ['tasks'] as const,
  task: (id: string) => ['tasks', id] as const,
  commandLine: (id: string) => ['tasks', id, 'command-line'] as const,
  overview: ['overview'] as const,
  runsAll: ['runs', 'list'] as const,
  runs: (f: RunFilter) => ['runs', 'list', f] as const,
  run: (id: string) => ['runs', 'one', id] as const,
  progress: (runId: string) => ['progress', runId] as const,
  snapshots: (taskId: string) => ['snapshots', taskId] as const,
}

const enc = encodeURIComponent

// ---- status, session, system, settings

// The wording follows the platform before anything shows the status.
export const fetchStatus = async () => {
  const s = await request<AppStatus>('GET', '/api/v1/status')
  applyPlatform(s.platform)
  return s
}

export const useStatus = () => useQuery({ queryKey: qk.status, queryFn: fetchStatus })

// useKnownStatus is the status the gate already fetched, kept current, for
// parts of a page that only read it: mounting them asks the server nothing.
export const useKnownStatus = () => useQuery({ queryKey: qk.status, queryFn: fetchStatus, refetchOnMount: false }).data

export const useSystem = () =>
  useQuery({ queryKey: qk.system, queryFn: () => request<SystemInfo>('GET', '/api/v1/system'), staleTime: 60_000 })

export const useSettings = () => useQuery({ queryKey: qk.settings, queryFn: () => request<Settings>('GET', '/api/v1/settings') })

export function useSaveSettings() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (s: Settings) => request<Settings>('PUT', '/api/v1/settings', s),
    onSuccess: (s) => qc.setQueryData(qk.settings, s),
  })
}

export const useChangePassword = () =>
  useMutation({
    mutationFn: (b: { current_password: string; new_password: string }) => request<void>('POST', '/api/v1/settings/password', b),
  })

export const fetchSession = () => request<CsrfToken>('GET', '/api/v1/session')

// ---- keys

export const useKeys = () => useQuery({ queryKey: qk.keys, queryFn: () => request<SSHKey[]>('GET', '/api/v1/ssh-keys') })

export function useCreateKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (b: { name: string; mode: 'generate' | 'import'; private_key?: string; passphrase?: string }) =>
      request<SSHKey>('POST', '/api/v1/ssh-keys', b),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.keys }),
  })
}

export function useRenameKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (b: { id: string; name: string; version: number }) =>
      request<SSHKey>('PUT', `/api/v1/ssh-keys/${enc(b.id)}`, { name: b.name, version: b.version }),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.keys }),
  })
}

export function useDeleteKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => request<void>('DELETE', `/api/v1/ssh-keys/${enc(id)}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.keys }),
  })
}

// ---- connections

export const useConnections = () =>
  useQuery({ queryKey: qk.connections, queryFn: () => request<Connection[]>('GET', '/api/v1/connections') })

export function useSaveConnection() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, ...b }: ConnectionInput & { id?: string }) =>
      id ? request<Connection>('PUT', `/api/v1/connections/${enc(id)}`, b) : request<Connection>('POST', '/api/v1/connections', b),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.connections }),
  })
}

export function useDeleteConnection() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => request<void>('DELETE', `/api/v1/connections/${enc(id)}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.connections }),
  })
}

export const useProbe = () =>
  useMutation({ mutationFn: (b: { host: string; port: number }) => request<ProbeResult>('POST', '/api/v1/connections/probe', b) })

export const useTestConnection = () =>
  useMutation({ mutationFn: (id: string) => request<TestResult>('POST', `/api/v1/connections/${enc(id)}/test`) })

// ---- tasks

export const useTasks = () => useQuery({ queryKey: qk.tasks, queryFn: () => request<Task[]>('GET', '/api/v1/tasks') })

export const useTask = (id: string | undefined) =>
  useQuery({ queryKey: qk.task(id ?? ''), queryFn: () => request<Task>('GET', `/api/v1/tasks/${enc(id!)}`), enabled: Boolean(id) })

export type TaskCreate = Omit<Task, 'id' | 'version' | 'created_at' | 'updated_at'> & {
  repo_password?: string
  recovery_confirmed?: boolean
}

export function useSaveTask() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (v: { id?: string; task: TaskCreate | Task }) =>
      v.id
        ? request<CreatedTask>('PUT', `/api/v1/tasks/${enc(v.id)}`, v.task)
        : request<CreatedTask>('POST', '/api/v1/tasks', v.task),
    onSuccess: (t) => {
      qc.setQueryData(qk.task(t.id), t)
      void qc.invalidateQueries({ queryKey: qk.tasks })
      void qc.invalidateQueries({ queryKey: qk.overview })
    },
  })
}

export function useDeleteTask() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => request<void>('DELETE', `/api/v1/tasks/${enc(id)}`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.tasks })
      void qc.invalidateQueries({ queryKey: qk.overview })
    },
  })
}

export function useSetTaskEnabled() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (v: { id: string; enabled: boolean }) =>
      request<Task>('POST', `/api/v1/tasks/${enc(v.id)}/enabled`, { enabled: v.enabled }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.tasks })
      void qc.invalidateQueries({ queryKey: qk.overview })
    },
  })
}

export function useRunTask() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (v: { id: string; kind?: RunKind; allow_deletes_once?: boolean }) =>
      request<RunStarted>('POST', `/api/v1/tasks/${enc(v.id)}/run`, {
        ...(v.kind ? { kind: v.kind } : {}),
        ...(v.allow_deletes_once ? { allow_deletes_once: true } : {}),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.overview })
      void qc.invalidateQueries({ queryKey: qk.runsAll })
    },
  })
}

export const useCommandLine = (id: string, enabled: boolean) =>
  useQuery({
    queryKey: qk.commandLine(id),
    queryFn: () => request<{ lines: string[] }>('GET', `/api/v1/tasks/${enc(id)}/command-line`),
    enabled,
    staleTime: 0,
  })

export const previewSchedule = (schedule: string) => request<SchedulePreview>('POST', '/api/v1/schedule/preview', { schedule })

export const generateRepoPassword = () => request<{ password: string }>('POST', '/api/v1/tasks/repo-password')

export const useRecoveryInfo = () =>
  useMutation({
    mutationFn: (v: { id: string; password: string }) =>
      request<Recovery>('POST', `/api/v1/tasks/${enc(v.id)}/recovery-info`, { password: v.password }),
  })

// ---- runs and overview

export const useOverview = () => useQuery({ queryKey: qk.overview, queryFn: () => request<Overview>('GET', '/api/v1/overview') })

export const RUNS_PAGE = 50

export function useRuns(f: RunFilter) {
  return useInfiniteQuery({
    queryKey: qk.runs(f),
    initialPageParam: '',
    queryFn: ({ pageParam }) => {
      const q = new URLSearchParams({ limit: String(RUNS_PAGE) })
      if (f.task_id) q.set('task_id', f.task_id)
      if (pageParam) q.set('before', pageParam)
      return request<Run[]>('GET', `/api/v1/runs?${q}`)
    },
    getNextPageParam: (last) => (last.length < RUNS_PAGE ? undefined : last[last.length - 1].started_at),
  })
}

export const useRun = (id: string) => useQuery({ queryKey: qk.run(id), queryFn: () => request<Run>('GET', `/api/v1/runs/${enc(id)}`) })

// useRunProgress is fed only by run.progress events (see events.ts); it never fetches.
export function useRunProgress(runId: string | undefined): Progress | undefined {
  const { data } = useQuery<Progress>({
    queryKey: qk.progress(runId ?? ''),
    queryFn: () => Promise.reject(new Error('progress comes from events')),
    enabled: false,
  })
  return data
}

export function useCancelRun() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => request<void>('POST', `/api/v1/runs/${enc(id)}/cancel`),
    onSuccess: (_d, id) => qc.invalidateQueries({ queryKey: qk.run(id) }),
  })
}

export const fetchRunLog = (id: string, offset: number) =>
  request<{ data: string; next_offset: number; eof: boolean }>('GET', `/api/v1/runs/${enc(id)}/log?offset=${offset}`)

// ---- snapshots

export const useSnapshots = (taskId: string) =>
  useQuery({
    queryKey: qk.snapshots(taskId),
    queryFn: () => request<SnapshotList>('GET', `/api/v1/tasks/${enc(taskId)}/snapshots`),
    enabled: Boolean(taskId),
    staleTime: 30_000,
  })

export const useRestore = () =>
  useMutation({
    mutationFn: (v: { id: string; snapshot_id: string; subpath?: string; target_dir: string }) =>
      request<RunStarted>('POST', `/api/v1/tasks/${enc(v.id)}/restore`, {
        snapshot_id: v.snapshot_id,
        target_dir: v.target_dir,
        ...(v.subpath ? { subpath: v.subpath } : {}),
      }),
  })

// forgetSession drops everything cached for the session that just ended,
// and asks whether another one still holds: on QTS, ending tern's own
// session leaves the QTS sign-in, and the gate brings the visitor back in.
export function forgetSession(qc: QueryClient) {
  qc.setQueryData<AppStatus>(qk.status, (s) => (s ? { ...s, authenticated: false } : s))
  qc.removeQueries({ predicate: (q) => q.queryKey[0] !== 'status' })
  void qc.invalidateQueries({ queryKey: qk.status })
}
