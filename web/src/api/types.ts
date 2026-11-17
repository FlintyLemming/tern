// The JSON of /api/v1, field for field.

export type Status =
  | 'success'
  | 'warning'
  | 'partial'
  | 'failed'
  | 'interrupted'
  | 'canceled'
  | 'skipped'
  | 'queued'
  | 'running'
  | 'retry_wait'

export const TERMINAL: Status[] = ['success', 'warning', 'partial', 'failed', 'interrupted', 'canceled', 'skipped']
export const isTerminal = (s: Status) => TERMINAL.includes(s)

export type VaultState = 'uninitialized' | 'unlocked' | 'locked' | 'keyfile_missing' | 'keyfile_mismatch'
export type ErrKind = '' | 'network' | 'unknown' | 'auth' | 'host_key' | 'config' | 'data' | 'guard'

// Whether /data and /secrets are volumes mounted into the container.
export interface Mounts {
  data: boolean
  secrets: boolean
}

export type Platform = 'docker' | 'qts'

export interface AppStatus {
  version: string
  setup_required: boolean
  vault_state: VaultState
  authenticated: boolean
  mounts?: Mounts
  platform?: Platform
  // how this browser is signed in, when authenticated: tern's own password
  // or, on QTS, a QTS administrator session (qts_user)
  signed_in_via?: 'password' | 'qts'
  qts_user?: string
}

export interface CsrfToken {
  csrf_token: string
}

export interface SSHKey {
  id: string
  version: number
  name: string
  public_key: string
  fingerprint: string
  has_passphrase: boolean
  created_at: string
  updated_at: string
}

export type ConnType = 'ssh' | 'webdav'

export interface Connection {
  id: string
  version: number
  type: ConnType
  name: string
  host: string
  port: number
  username: string
  ssh_key_id: string
  host_key: string
  host_key_fingerprint: string
  url: string
  has_password: boolean
  created_at: string
  updated_at: string
}

export interface ConnectionInput {
  type: ConnType
  name: string
  host?: string
  port?: number
  username?: string
  ssh_key_id?: string
  host_key?: string
  url?: string
  password?: string // write-only; absent on update keeps the stored one
  version?: number
}

export interface ProbeResult {
  type: string
  authorized: string
  fingerprint: string
}

export interface TestResult {
  ok: boolean
  kind: ErrKind
  message: string
}

export type TaskType = 'backup' | 'sync'

export interface Hook {
  script: string
  where: 'local' | 'ssh'
  connection_id: string
  timeout_sec: number
  ignore_failure: boolean
}

export interface KeepPolicy {
  last: number
  daily: number
  weekly: number
  monthly: number
}

export interface BackupSpec {
  connection_id: string
  repo_path: string
  host: string
  sources: string[]
  excludes: string[]
  limit_upload_kib: number
  connections: number
  keep: KeepPolicy
  prune_every_days: number
  check_every_days: number
}

export type Protocol = 'sftp' | 'rsync' | 'webdav'

export interface FolderPair {
  local: string
  remote: string
}

export interface SyncSpec {
  protocol: Protocol
  connection_id: string
  direction: 'push' | 'pull'
  pairs: FolderPair[]
  delete_mode: 'keep' | 'mirror'
  max_deletes: number
  excludes: string[]
  compare: 'size_mtime' | 'checksum'
  limit_kib: number
  transfers: number
}

export interface Task {
  id: string
  version: number
  type: TaskType
  name: string
  enabled: boolean
  schedule: string
  catch_up: boolean
  stale_after_sec: number
  max_runtime_sec: number
  retry: { retries: number; delays_sec: number[] }
  pre: Hook
  post: Hook
  backup?: BackupSpec
  sync?: SyncSpec
  created_at: string
  updated_at: string
}

export type RepoState = 'new' | 'existing'
export type CreatedTask = Task & { repo_state?: RepoState }

export type RunKind = 'backup' | 'sync' | 'dry_run' | 'restore' | 'prune' | 'check'
export type Trigger = 'schedule' | 'manual' | 'catch_up'

export interface UnitResult {
  name: string
  status: Status
  bytes: number
  files: number
  error: string
}

export interface Progress {
  unit: string
  bytes_done: number
  bytes_total: number
  files_done: number
  files_total: number
  speed_bps: number
  eta_sec: number
}

export interface Run {
  id: string
  seq: number
  task_id: string
  task_name: string
  kind: RunKind
  trigger: Trigger
  status: Status
  started_at: string
  finished_at: string | null
  attempts: number
  bytes: number
  files: number
  error_kind: ErrKind
  error_summary: string
  units: UnitResult[]
}

export interface RunLog {
  data: string
  next_offset: number
  eof: boolean
}

export interface OverviewItem {
  task_id: string
  name: string
  type: TaskType
  enabled: boolean
  last_status: Status | ''
  last_run_at: string | null
  last_success_at: string | null
  next_run_at: string | null
  stale: boolean
  stale_after_sec: number
  active_run: { run_id: string; status: Status; progress: Progress | null } | null
  last_prune_at?: string | null
  last_check_at?: string | null
  last_check_status?: Status | ''
}

export interface Overview {
  tasks: OverviewItem[]
}

export interface Settings {
  node_name: string
  max_concurrent: number
  log_keep_days: number
  log_keep_runs: number
}

export interface BBR {
  available: string[] | null
  current: string
  supported: boolean
  active: boolean
}

export interface Tool {
  name: string
  version: string
  found: boolean
}

export interface SystemInfo {
  version: string
  timezone: string
  bbr: BBR
  tools: Tool[]
  mounts: Mounts
}

export interface Snapshot {
  id: string
  short_id: string
  time: string
  host: string
  paths: string[]
  tags: string[] | null
  mine: boolean
}

export interface SnapshotList {
  total: number
  snapshots: Snapshot[]
  repository: string
}

export interface Recovery {
  repository: string
  password: string
  commands: string[]
}

export interface RunStarted {
  run_id: string
}

export interface SchedulePreview {
  next: string[]
  period_sec: number
}
