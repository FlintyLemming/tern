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
