import { screen, waitFor, within } from '@testing-library/react'
import { Route } from 'react-router'
import { afterEach } from 'vitest'
import { fakeFetch, type Reply } from '../../test/fetch'
import { davConn, key, sshConn, system } from '../../test/data'
import { inMutationCache, renderRoutes } from '../../test/render'
import { choose } from '../../test/select'
import { ConnectionsPage } from './ConnectionsPage'

afterEach(() => vi.unstubAllGlobals())

const PRIV = '-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\n'

function renderPage(routes: Record<string, Reply | Reply[]> = {}, tab?: 'keys' | 'connections') {
  const f = fakeFetch({
    'GET /api/v1/system': { body: system },
    'GET /api/v1/ssh-keys': { body: [key] },
    'GET /api/v1/connections': { body: [sshConn, davConn] },
    ...routes,
  })
  const r = renderRoutes(<Route path="/connections" element={<ConnectionsPage />} />, tab === 'keys' ? '/connections?tab=keys' : '/connections')
  return { f, ...r }
}

const probe = { type: 'ssh-ed25519', authorized: 'ssh-ed25519 AAAAprobed', fingerprint: 'SHA256:probedfp' }

it('keys: generated key shows public key and copy button', async () => {
  const created = { ...key, id: 'k2', name: 'fresh', public_key: 'ssh-ed25519 AAAAfresh tern' }
  const { f, user } = renderPage({ 'POST /api/v1/ssh-keys': { status: 201, body: created } }, 'keys')
  await user.click(await screen.findByRole('button', { name: '生成密钥' }))
  const dlg = await screen.findByRole('dialog')
  await user.type(within(dlg).getByLabelText('名称'), 'fresh')
  await user.click(within(dlg).getByRole('button', { name: '生成' }))
  expect(await within(dlg).findByText('ssh-ed25519 AAAAfresh tern')).toBeInTheDocument()
  expect(within(dlg).getByText('把这一行加到对端的 ~/.ssh/authorized_keys')).toBeInTheDocument()
  expect(within(dlg).getByRole('button', { name: '复制' })).toBeInTheDocument()
  expect(f.of('POST', '/api/v1/ssh-keys')[0].body).toEqual({ name: 'fresh', mode: 'generate' })
})

it('keys: no private key anywhere after import', async () => {
  const { f, user } = renderPage({ 'POST /api/v1/ssh-keys': { status: 201, body: { ...key, id: 'k3', name: 'imported' } } }, 'keys')
  await user.click(await screen.findByRole('button', { name: '导入密钥' }))
  const dlg = await screen.findByRole('dialog')
  await user.type(within(dlg).getByLabelText('名称'), 'imported')
  await user.click(within(dlg).getByLabelText('私钥'))
  await user.paste(PRIV)
  await user.type(within(dlg).getByLabelText('私钥口令（可选）'), 'kp')
  expect(within(dlg).getByLabelText('私钥口令（可选）')).toHaveAttribute('type', 'password')
  await user.click(within(dlg).getByRole('button', { name: '导入' }))
  await waitFor(() => expect(f.of('POST', '/api/v1/ssh-keys')).toHaveLength(1))
  expect(f.of('POST', '/api/v1/ssh-keys')[0].body).toEqual({ name: 'imported', mode: 'import', private_key: PRIV, passphrase: 'kp' })
  await screen.findByText(key.public_key)
  expect(document.body.innerHTML).not.toContain('PRIVATE KEY')
  expect(screen.queryByDisplayValue(/PRIVATE KEY/)).toBeNull()
  const ta = screen.queryByLabelText('私钥')
  if (ta) expect(ta).toHaveValue('')
})

it('keys: list never shows a private key and offers the public one', async () => {
  const { user } = renderPage({}, 'keys')
  const row = (await screen.findByText('nas key')).closest('tr')!
  expect(within(row).getByText('SHA256:keyfp')).toBeInTheDocument()
  expect(within(row).getByText('否')).toBeInTheDocument()
  expect(await within(row).findByText('2026-11-11 03:00')).toBeInTheDocument() // node zone, not the browser's
  const spy = vi.spyOn(navigator.clipboard, 'writeText')
  await user.click(within(row).getByRole('button', { name: '复制公钥' }))
  expect(spy).toHaveBeenCalledWith(key.public_key)
})

it('keys: delete in use shows guidance', async () => {
  const { user } = renderPage({ 'DELETE /api/v1/ssh-keys/k1': { status: 409, body: { error: { code: 'in_use', message: 'the ssh key is still in use' } } } }, 'keys')
  const row = (await screen.findByText('nas key')).closest('tr')!
  await user.click(within(row).getByRole('button', { name: '删除' }))
  const dlg = await screen.findByRole('dialog')
  await user.click(within(dlg).getByRole('button', { name: '删除' }))
  expect(await within(dlg).findByText(/先删掉或改掉用它的连接/)).toBeInTheDocument()
})

async function openNewSSH(user: ReturnType<typeof renderPage>['user']) {
  await user.click(await screen.findByRole('button', { name: '新建 SSH 连接' }))
  const dlg = await screen.findByRole('dialog')
  await user.type(within(dlg).getByLabelText('名称'), 'nas3')
  await user.type(within(dlg).getByLabelText('主机'), 'nas3.example')
  await user.type(within(dlg).getByLabelText('用户名'), 'backup')
  await choose(user, '密钥', 'nas key', dlg)
  return dlg
}

it('ssh: save disabled until fingerprint confirmed', async () => {
  const { user } = renderPage({ 'POST /api/v1/connections/probe': { body: probe } })
  const dlg = await openNewSSH(user)
  expect(within(dlg).getByLabelText('端口')).toHaveValue('22')
  const save = within(dlg).getByRole('button', { name: '保存' })
  expect(save).toBeDisabled()
  await user.click(within(dlg).getByRole('button', { name: '获取指纹' }))
  expect(await within(dlg).findByText('SHA256:probedfp')).toBeInTheDocument()
  expect(within(dlg).getByText('ssh-ed25519')).toBeInTheDocument()
  expect(save).toBeDisabled()
  await user.click(within(dlg).getByLabelText('我已在对端核对过这个指纹'))
  expect(save).toBeEnabled()
})

it('ssh: submits probed authorized key as host_key', async () => {
  const { f, user } = renderPage({
    'POST /api/v1/connections/probe': { body: probe },
    'POST /api/v1/connections': { status: 201, body: { ...sshConn, id: 'c9' } },
  })
  const dlg = await openNewSSH(user)
  await user.click(within(dlg).getByRole('button', { name: '获取指纹' }))
  await user.click(await within(dlg).findByLabelText('我已在对端核对过这个指纹'))
  await user.click(within(dlg).getByRole('button', { name: '保存' }))
  await waitFor(() => expect(f.of('POST', '/api/v1/connections')).toHaveLength(1))
  expect(f.of('POST', '/api/v1/connections/probe')[0].body).toEqual({ host: 'nas3.example', port: 22 })
  expect(f.of('POST', '/api/v1/connections')[0].body).toEqual({
    type: 'ssh', name: 'nas3', host: 'nas3.example', port: 22, username: 'backup', ssh_key_id: 'k1', host_key: 'ssh-ed25519 AAAAprobed',
  })
})

it('ssh: changing host clears confirmation', async () => {
  const { f, user } = renderPage({ 'PUT /api/v1/connections/c1': { body: sshConn } })
  const row = (await screen.findByText('nas2')).closest('tr')!
  await user.click(within(row).getByRole('button', { name: '编辑' }))
  const dlg = await screen.findByRole('dialog')
  // the stored fingerprint counts as confirmed while host and port stay
  expect(within(dlg).getByText('SHA256:hostfp')).toBeInTheDocument()
  const save = within(dlg).getByRole('button', { name: '保存' })
  expect(save).toBeEnabled()
  await user.type(within(dlg).getByLabelText('主机'), 'x')
  expect(within(dlg).queryByText('SHA256:hostfp')).toBeNull()
  expect(save).toBeDisabled()
  expect(f.of('PUT', '/api/v1/connections/c1')).toHaveLength(0)
})

it('ssh: unchanged edit keeps the stored host key and version', async () => {
  const { f, user } = renderPage({ 'PUT /api/v1/connections/c1': { body: sshConn } })
  const row = (await screen.findByText('nas2')).closest('tr')!
  await user.click(within(row).getByRole('button', { name: '编辑' }))
  const dlg = await screen.findByRole('dialog')
  await user.clear(within(dlg).getByLabelText('名称'))
  await user.type(within(dlg).getByLabelText('名称'), 'nas2b')
  await user.click(within(dlg).getByRole('button', { name: '保存' }))
  await waitFor(() => expect(f.of('PUT', '/api/v1/connections/c1')).toHaveLength(1))
  expect(f.of('PUT', '/api/v1/connections/c1')[0].body).toEqual({
    type: 'ssh', name: 'nas2b', host: 'nas2.example', port: 22, username: 'backup', ssh_key_id: 'k1', host_key: 'ssh-ed25519 AAAAhostkey', version: 3,
  })
})

it('ssh: probe failure shows unreachable message', async () => {
  const { user } = renderPage({ 'POST /api/v1/connections/probe': { status: 502, body: { error: { code: 'unreachable', message: 'dial tcp: i/o timeout' } } } })
  const dlg = await openNewSSH(user)
  await user.click(within(dlg).getByRole('button', { name: '获取指纹' }))
  expect(await within(dlg).findByText('连不上对端。检查地址，以及组网是否通')).toBeInTheDocument()
  expect(within(dlg).getByRole('button', { name: '保存' })).toBeDisabled()
})

it('webdav: password never prefilled', async () => {
  const { user } = renderPage()
  const row = (await screen.findByText('dav')).closest('tr')!
  await user.click(within(row).getByRole('button', { name: '编辑' }))
  const dlg = await screen.findByRole('dialog')
  const pw = within(dlg).getByLabelText('密码')
  expect(pw).toHaveValue('')
  expect(pw).toHaveAttribute('type', 'password')
  expect(pw).toHaveAttribute('placeholder', '已保存，留空表示不修改')
})

it('webdav: empty password omitted on update', async () => {
  const { f, user } = renderPage({ 'PUT /api/v1/connections/c2': { body: davConn } })
  const row = (await screen.findByText('dav')).closest('tr')!
  await user.click(within(row).getByRole('button', { name: '编辑' }))
  const dlg = await screen.findByRole('dialog')
  await user.click(within(dlg).getByRole('button', { name: '保存' }))
  await waitFor(() => expect(f.of('PUT', '/api/v1/connections/c2')).toHaveLength(1))
  const body = f.of('PUT', '/api/v1/connections/c2')[0].body as Record<string, unknown>
  expect('password' in body).toBe(false)
  expect(body).toEqual({ type: 'webdav', name: 'dav', url: davConn.url, username: 'alice', version: 2 })
})

it('webdav: a new password is sent', async () => {
  const { f, user } = renderPage({ 'POST /api/v1/connections': { status: 201, body: davConn } })
  await user.click(await screen.findByRole('button', { name: '新建 WebDAV 连接' }))
  const dlg = await screen.findByRole('dialog')
  await user.type(within(dlg).getByLabelText('名称'), 'dav2')
  await user.type(within(dlg).getByLabelText('地址'), 'https://dav2.example/')
  await user.type(within(dlg).getByLabelText('用户名'), 'bob')
  await user.type(within(dlg).getByLabelText('密码'), 's3cret')
  await user.click(within(dlg).getByRole('button', { name: '保存' }))
  await waitFor(() => expect(f.of('POST', '/api/v1/connections')).toHaveLength(1))
  expect(f.of('POST', '/api/v1/connections')[0].body).toEqual({ type: 'webdav', name: 'dav2', url: 'https://dav2.example/', username: 'bob', password: 's3cret' })
})

it('shows server field errors under the inputs', async () => {
  const { user } = renderPage({
    'POST /api/v1/connections': { status: 400, body: { error: { code: 'validation', message: 'invalid connection', fields: { url: 'embedded_credentials' } } } },
  })
  await user.click(await screen.findByRole('button', { name: '新建 WebDAV 连接' }))
  const dlg = await screen.findByRole('dialog')
  await user.type(within(dlg).getByLabelText('名称'), 'dav2')
  await user.type(within(dlg).getByLabelText('地址'), 'https://u:p@dav2.example/')
  await user.click(within(dlg).getByRole('button', { name: '保存' }))
  expect(await within(dlg).findByText('地址里不能带用户名或密码')).toBeInTheDocument()
  expect(within(dlg).getByLabelText('地址')).toHaveAttribute('aria-invalid', 'true')
})

const testRows: [string, { ok: boolean; kind: string; message: string }, string][] = [
  ['ok', { ok: true, kind: '', message: '' }, '连接正常'],
  ['network', { ok: false, kind: 'network', message: 'dial tcp: timeout' }, '连不上对端。检查地址，以及组网是否通'],
  ['auth', { ok: false, kind: 'auth', message: 'ssh: unable to authenticate' }, '对端拒绝了认证。检查公钥是否已加到对端，或 WebDAV 的用户名密码'],
  ['host_key', { ok: false, kind: 'host_key', message: 'host key mismatch' }, '对端的指纹变了。如果你没有重装过对端，这可能是有人在中间冒充。确认无误后，编辑这个连接重新获取并确认指纹'],
  ['config', { ok: false, kind: 'config', message: 'remote path /x does not exist' }, 'remote path /x does not exist'],
]

it.each(testRows)('test connection: shows message per kind (%s)', async (_k, result, text) => {
  const { user } = renderPage({ 'POST /api/v1/connections/c1/test': { body: result } })
  const row = (await screen.findByText('nas2')).closest('tr')!
  await user.click(within(row).getByRole('button', { name: '测试连接' }))
  expect(await screen.findByText(text)).toBeInTheDocument()
})

it('keys: import leaves no private key or passphrase in memory', async () => {
  const { user, qc } = renderPage({ 'POST /api/v1/ssh-keys': { status: 201, body: { ...key, id: 'k3', name: 'imported' } } }, 'keys')
  await user.click(await screen.findByRole('button', { name: '导入密钥' }))
  const dlg = await screen.findByRole('dialog')
  await user.type(within(dlg).getByLabelText('名称'), 'imported')
  await user.click(within(dlg).getByLabelText('私钥'))
  await user.paste(PRIV)
  await user.type(within(dlg).getByLabelText('私钥口令（可选）'), 'key-passphrase-xyz')
  await user.click(within(dlg).getByRole('button', { name: '导入' }))
  await screen.findByText(key.public_key)
  await waitFor(() => expect(inMutationCache(qc, 'PRIVATE KEY')).toBe(false))
  expect(inMutationCache(qc, 'key-passphrase-xyz')).toBe(false)
})
