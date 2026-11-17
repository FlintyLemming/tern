import zh from './zh.json'
import en from './en.json'
import zhQTS from './zh.qts.json'
import enQTS from './en.qts.json'
import i18n, { applyPlatform } from '.'

function keys(o: unknown, prefix = ''): string[] {
  if (typeof o !== 'object' || o === null) return [prefix]
  return Object.entries(o).flatMap(([k, v]) => keys(v, prefix ? `${prefix}.${k}` : k))
}

const has = (o: unknown, path: string) => keys(o).includes(path)

it('zh and en have identical keys', () => {
  expect(keys(zh).sort()).toEqual(keys(en).sort())
})

it('has no empty strings', () => {
  for (const lang of [zh, en]) {
    for (const k of keys(lang)) {
      const v = k.split('.').reduce<unknown>((o, p) => (o as Record<string, unknown>)[p], lang)
      expect(v, k).not.toBe('')
    }
  }
})

// Every status, every top-level error code the API sends (plans 1–5), every
// field error code, and every error kind of a run.
const statuses = ['success', 'warning', 'partial', 'failed', 'interrupted', 'canceled', 'skipped', 'queued', 'running', 'retry_wait']
const errorCodes = [
  'validation', 'bad_request', 'not_found', 'conflict', 'in_use', 'unauthorized', 'forbidden', 'csrf', 'rate_limited',
  'internal', 'already_setup', 'vault_locked', 'vault_unavailable', 'host_key_required', 'passphrase_required',
  'bad_passphrase', 'unsupported_key', 'unreachable', 'task_active', 'paused', 'recovery_not_confirmed',
  'wrong_repo_password', 'unavailable', 'network', 'qts_admin_required',
]
const fieldCodes = [
  'required', 'invalid', 'too_long', 'too_short', 'not_found', 'not_allowed', 'immutable', 'overlaps', 'never_fires',
  'wrong_type', 'embedded_credentials', 'scheme', 'not_absolute', 'not_empty', 'not_mounted', 'not_writable',
  'inside_source', 'inside_data',
]
const kinds = ['network', 'unknown', 'auth', 'host_key', 'config', 'data', 'guard']

it('has a label for every status and error code', () => {
  for (const lang of [zh, en]) {
    for (const s of statuses) expect(has(lang, `status.${s}`), s).toBe(true)
    for (const c of errorCodes) expect(has(lang, `error.${c}`), c).toBe(true)
    for (const c of fieldCodes) expect(has(lang, `field.${c}`), c).toBe(true)
    for (const k of kinds) expect(has(lang, `kind.${k}`), k).toBe(true)
  }
})

// The QNAP wording replaces strings; it never brings keys of its own.
it('QTS overlays only replace existing keys, the same in both languages', () => {
  expect(keys(zhQTS).sort()).toEqual(keys(enQTS).sort())
  for (const k of keys(zhQTS)) {
    expect(has(zh, k), k).toBe(true)
    expect(has(en, k), k).toBe(true)
  }
})

it('switches the wording with the platform', async () => {
  const t = () => i18n.t('task.hook_local')
  expect(t()).toBe('容器内')
  applyPlatform('qts')
  expect(t()).toBe('本机（NAS）')
  expect(i18n.t('setup.node_name')).toBe('节点名') // keys without a QTS text stay
  await i18n.changeLanguage('en')
  expect(t()).toBe('On this NAS')
  applyPlatform('docker')
  expect(t()).toBe('Inside the container')
  applyPlatform(undefined) // an older server says nothing: docker
  expect(t()).toBe('Inside the container')
})
