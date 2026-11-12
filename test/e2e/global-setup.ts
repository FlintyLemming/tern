import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

// Starts the acceptance stack for the end-to-end tests and hands the tests
// what they need through the environment: TERN_E2E_URL, TERN_E2E_DIR (the
// host directories bound into the containers), TERN_E2E_HOSTKEY_FP (the
// fingerprint the SSH server's host key really has).
export default async function globalSetup() {
  const root = mkdtempSync(join(tmpdir(), 'tern-e2e-'))
  const dirs: Record<string, string> = {}
  for (const d of ['data', 'secrets', 'sources', 'targets', 'remote', 'keys', 'hostkey']) {
    dirs[d] = join(root, d)
    mkdirSync(dirs[d], { recursive: true })
  }
  const hostKey = join(dirs.hostkey, 'ssh_host_ed25519_key')
  execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-C', 'tern-e2e', '-f', hostKey])
  const fp = execFileSync('ssh-keygen', ['-l', '-E', 'sha256', '-f', `${hostKey}.pub`]).toString().split(' ')[1]

  writeFileSync(join(dirs.keys, 'authorized_keys'), '')
  const src = join(dirs.sources, 'e2e')
  mkdirSync(join(src, 'sub'), { recursive: true })
  writeFileSync(join(src, 'a.txt'), 'alpha\n')
  writeFileSync(join(src, 'sub', 'b.txt'), 'beta\n')
  writeFileSync(join(src, '照片.jpg'), 'not really a jpeg')

  const project = `tern-e2e-${process.pid}`
  const compose = resolve(import.meta.dirname, '..', 'acceptance', 'host', 'compose.yaml')
  const env = {
    ...process.env,
    TERN_VERSION: process.env.TERN_VERSION ?? 'acceptance',
    TERN_ACC_DATA: dirs.data,
    TERN_ACC_SECRETS: dirs.secrets,
    TERN_ACC_SOURCES: dirs.sources,
    TERN_ACC_TARGETS: dirs.targets,
    TERN_ACC_REMOTE: dirs.remote,
    TERN_ACC_KEYS: dirs.keys,
    TERN_ACC_HOSTKEY: hostKey,
    TERN_ACC_DAV_PASS: 'e2e-dav-password',
  }
  const dc = (...args: string[]) => execFileSync('docker', ['compose', '-p', project, '-f', compose, ...args], { env, stdio: ['ignore', 'pipe', 'inherit'] }).toString()
  dc('up', '-d', '--wait', '--wait-timeout', '120')
  const port = dc('port', 'tern', '8080').trim()

  process.env.TERN_E2E_URL = `http://${port}`
  process.env.TERN_E2E_DIR = root
  process.env.TERN_E2E_HOSTKEY_FP = fp

  return async () => {
    if (process.env.TERN_E2E_KEEP) {
      console.log(`kept the stack ${project} and ${root}`)
      return
    }
    dc('down', '-v', '--remove-orphans', '-t', '5')
    rmSync(root, { recursive: true, force: true })
  }
}
