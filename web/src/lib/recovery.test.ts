import { recoveryRepository, recoveryText } from './recovery'

it('recoveryRepository', () => {
  expect(recoveryRepository({ username: 'backup', host: 'nas2.example', port: 22 }, '/srv/repo')).toBe('sftp:backup@nas2.example:/srv/repo')
  expect(recoveryRepository({ username: 'backup', host: 'nas2.example', port: 2222 }, '/srv/repo')).toBe('sftp://backup@nas2.example:2222//srv/repo')
  expect(recoveryRepository({ username: 'backup', host: 'fd7a:115c::1', port: 22 }, '/srv/repo')).toBe('sftp://backup@[fd7a:115c::1]:22//srv/repo')
})

it('recoveryText holds the repository, the password and restore commands', () => {
  const s = recoveryText({ task: 'docs', repository: "sftp:backup@nas2.example:/srv/my repo", password: 'pw' })
  expect(s).toContain('sftp:backup@nas2.example:/srv/my repo')
  expect(s).toContain('pw')
  expect(s).toContain("restic -r 'sftp:backup@nas2.example:/srv/my repo' snapshots")
  expect(s).toContain("restic -r 'sftp:backup@nas2.example:/srv/my repo' restore latest --target /path/to/restore")
})
