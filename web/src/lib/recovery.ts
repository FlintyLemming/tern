// The repository address restic takes, from the connection and the path on
// it — the same rule as restic.RecoveryRepository on the server.
export function recoveryRepository(conn: { username: string; host: string; port: number }, repoPath: string): string {
  const ipv6 = conn.host.includes(':')
  if (conn.port === 22 && !ipv6) return `sftp:${conn.username}@${conn.host}:${repoPath}`
  const host = ipv6 ? `[${conn.host}]` : conn.host
  return `sftp://${conn.username}@${host}:${conn.port}/${repoPath}`
}

// shQuote writes s as one word of a POSIX shell command line.
export function shQuote(s: string): string {
  if (s !== '' && /^[A-Za-z0-9_@%+=:,./-]+$/.test(s)) return s
  return `'${s.replaceAll("'", `'"'"'`)}'`
}

// recoveryText is the file a person keeps away from this machine: enough to
// get the data back with nothing but restic and the SSH key.
export function recoveryText(o: { task: string; repository: string; password: string }): string {
  const repo = shQuote(o.repository)
  return [
    'tern recovery information',
    '',
    `Task: ${o.task}`,
    `Repository: ${o.repository}`,
    `Password: ${o.password}`,
    '',
    'Restore without tern (restic asks for the password above; SSH needs the key that reaches the repository host):',
    `restic -r ${repo} snapshots`,
    `restic -r ${repo} restore latest --target /path/to/restore`,
    '',
  ].join('\n')
}
