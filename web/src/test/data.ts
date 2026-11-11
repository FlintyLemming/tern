// Canned API objects for page tests.
import type { Connection, SSHKey, SystemInfo } from '../api/types'

export const system: SystemInfo = {
  version: 'dev',
  timezone: 'Asia/Shanghai',
  bbr: { available: ['reno', 'cubic', 'bbr'], current: 'bbr', supported: true, active: true },
  tools: [
    { name: 'restic', version: '0.19.1', found: true },
    { name: 'rclone', version: '1.76.0', found: true },
    { name: 'rsync', version: '3.5.2', found: true },
    { name: 'ssh', version: 'OpenSSH_10.0', found: true },
  ],
}

export const key: SSHKey = {
  id: 'k1',
  version: 1,
  name: 'nas key',
  public_key: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK0 tern',
  fingerprint: 'SHA256:keyfp',
  has_passphrase: false,
  created_at: '2026-11-10T19:00:00Z',
  updated_at: '2026-11-10T19:00:00Z',
}

export const sshConn: Connection = {
  id: 'c1',
  version: 3,
  type: 'ssh',
  name: 'nas2',
  host: 'nas2.example',
  port: 22,
  username: 'backup',
  ssh_key_id: 'k1',
  host_key: 'ssh-ed25519 AAAAhostkey',
  host_key_fingerprint: 'SHA256:hostfp',
  url: '',
  has_password: false,
  created_at: '2026-11-10T19:00:00Z',
  updated_at: '2026-11-10T19:00:00Z',
}

export const davConn: Connection = {
  id: 'c2',
  version: 2,
  type: 'webdav',
  name: 'dav',
  host: '',
  port: 0,
  username: 'alice',
  ssh_key_id: '',
  host_key: '',
  host_key_fingerprint: '',
  url: 'https://dav.example/remote.php/dav/files/alice',
  has_password: true,
  created_at: '2026-11-10T19:00:00Z',
  updated_at: '2026-11-10T19:00:00Z',
}

import type { Hook, Overview, Task } from '../api/types'

export const hook: Hook = { script: '', where: 'local', connection_id: '', timeout_sec: 600, ignore_failure: false }

export const syncTask: Task = {
  id: 't2',
  version: 4,
  type: 'sync',
  name: 'photos',
  enabled: true,
  schedule: '0 3 * * *',
  catch_up: false,
  stale_after_sec: 0,
  max_runtime_sec: 0,
  retry: { retries: 3, delays_sec: [60, 300, 900] },
  pre: hook,
  post: hook,
  sync: {
    protocol: 'sftp',
    connection_id: 'c1',
    direction: 'push',
    pairs: [
      { local: '/data/photos', remote: '/srv/photos' },
      { local: '/data/videos', remote: '/srv/videos' },
    ],
    delete_mode: 'keep',
    max_deletes: 100,
    excludes: [],
    compare: 'size_mtime',
    limit_kib: 0,
    transfers: 4,
  },
  created_at: '2026-11-01T00:00:00Z',
  updated_at: '2026-11-01T00:00:00Z',
}

export const backupTask: Task = {
  id: 't1',
  version: 2,
  type: 'backup',
  name: 'docs',
  enabled: true,
  schedule: '0 3 * * *',
  catch_up: false,
  stale_after_sec: 0,
  max_runtime_sec: 0,
  retry: { retries: 3, delays_sec: [60, 300, 900] },
  pre: hook,
  post: hook,
  backup: {
    connection_id: 'c1',
    repo_path: '/srv/repo',
    host: 'nas-home',
    sources: ['/data/docs'],
    excludes: ['*.tmp'],
    limit_upload_kib: 0,
    connections: 5,
    keep: { last: 0, daily: 7, weekly: 4, monthly: 12 },
    prune_every_days: 7,
    check_every_days: 30,
  },
  created_at: '2026-11-01T00:00:00Z',
  updated_at: '2026-11-01T00:00:00Z',
}

export const overview: Overview = {
  tasks: [
    {
      task_id: 't1', name: 'docs', type: 'backup', enabled: true, last_status: 'success', last_run_at: '2026-11-10T19:00:00Z',
      last_success_at: '2026-11-10T19:00:00Z', next_run_at: '2026-11-11T19:00:00Z', stale: false, stale_after_sec: 172800, active_run: null,
      last_prune_at: null, last_check_at: null, last_check_status: '',
    },
    {
      task_id: 't2', name: 'photos', type: 'sync', enabled: true, last_status: 'failed', last_run_at: '2026-11-10T19:00:00Z',
      last_success_at: null, next_run_at: '2026-11-11T19:00:00Z', stale: false, stale_after_sec: 172800, active_run: null,
    },
  ],
}

export const fiveTimes = ['2026-11-11T19:00:00Z', '2026-11-12T19:00:00Z', '2026-11-13T19:00:00Z', '2026-11-14T19:00:00Z', '2026-11-15T19:00:00Z']
