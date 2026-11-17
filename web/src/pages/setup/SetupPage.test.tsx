import { screen, waitFor } from '@testing-library/react'
import { Route } from 'react-router'
import { afterEach } from 'vitest'
import { fakeFetch } from '../../test/fetch'
import { renderRoutes } from '../../test/render'
import { SetupPage } from './SetupPage'

afterEach(() => vi.unstubAllGlobals())

const good = 'correct horse battery'

function renderSetup(reply: Parameters<typeof fakeFetch>[0][string] = { status: 201, body: { csrf_token: 'c' } }, mounts = { data: true, secrets: true }) {
  // What the server says before and, once a setup request went out, after.
  const status = () => {
    const done = f.of('POST', '/api/v1/setup').length > 0
    return { body: { version: 'test', setup_required: !done, vault_state: done ? 'unlocked' : 'uninitialized', authenticated: done, mounts } }
  }
  const f = fakeFetch({ 'GET /api/v1/status': status, 'POST /api/v1/setup': reply })
  const r = renderRoutes(<Route path="/setup" element={<SetupPage />} />, '/setup')
  return { f, ...r }
}

async function fill(user: ReturnType<typeof renderSetup>['user'], pw = good, again = pw, node = 'nas-home') {
  await user.type(screen.getByLabelText('管理员密码'), pw)
  await user.type(screen.getByLabelText('再输一遍'), again)
  await user.clear(screen.getByLabelText('节点名'))
  await user.type(screen.getByLabelText('节点名'), node)
  await user.click(screen.getByRole('button', { name: '完成初始化' }))
}

it('setup: asks for one password and nothing else secret', async () => {
  const { f } = renderSetup()
  await waitFor(() => expect(f.of('GET', '/api/v1/status')).toHaveLength(1))
  expect(document.querySelectorAll('input[type="password"]')).toHaveLength(2) // the password and its repetition
  expect(screen.queryByText(/口令/)).toBeNull()
  expect(screen.queryByRole('radio')).toBeNull()
})

it('setup: rejects short or mismatched passwords locally', async () => {
  const { f, user } = renderSetup()
  await fill(user, '123456789')
  expect(screen.getByText('至少 10 个字符')).toBeInTheDocument()
  await user.clear(screen.getByLabelText('管理员密码'))
  await user.clear(screen.getByLabelText('再输一遍'))
  await user.type(screen.getByLabelText('管理员密码'), good)
  await user.type(screen.getByLabelText('再输一遍'), good + 'x')
  await user.click(screen.getByRole('button', { name: '完成初始化' }))
  expect(screen.getByText('两次输入不一致')).toBeInTheDocument()
  expect(f.of('POST', '/api/v1/setup')).toHaveLength(0)
})

it('setup: rejects a bad node name locally', async () => {
  const { f, user } = renderSetup()
  await fill(user, good, good, 'NAS Home')
  expect(screen.getByText(/小写字母/, { selector: '.text-danger' })).toBeInTheDocument()
  expect(f.of('POST', '/api/v1/setup')).toHaveLength(0)
})

it('setup: submits the expected body', async () => {
  const { f, user } = renderSetup()
  await fill(user)
  await waitFor(() => expect(f.of('POST', '/api/v1/setup')).toHaveLength(1))
  expect(f.of('POST', '/api/v1/setup')[0].body).toEqual({ password: good, node_name: 'nas-home' })
})

it('setup: shows server field errors', async () => {
  const { user } = renderSetup({ status: 400, body: { error: { code: 'validation', message: 'invalid setup request', fields: { node_name: 'invalid' } } } })
  await fill(user)
  const node = screen.getByLabelText('节点名')
  await waitFor(() => expect(node).toHaveAttribute('aria-invalid', 'true'))
  const err = document.getElementById(node.getAttribute('aria-describedby')!.split(' ')[0])
  expect(err).toHaveTextContent('格式不对')
})

it('setup: shows other server errors above the button', async () => {
  const { user } = renderSetup({ status: 409, body: { error: { code: 'already_setup', message: 'this node has already been set up' } } })
  await fill(user)
  expect(await screen.findByText(/已经初始化/)).toBeInTheDocument()
})

it('setup: clears secrets from state after success', async () => {
  const { f, user, qc } = renderSetup()
  await fill(user)
  await waitFor(() => expect(f.of('POST', '/api/v1/setup')).toHaveLength(1))
  await waitFor(() => expect(qc.getQueryData(['status'])).toMatchObject({ setup_required: false, authenticated: true }))
  expect(screen.getByLabelText('管理员密码')).toHaveValue('')
  expect(screen.getByLabelText('再输一遍')).toHaveValue('')
})

it('setup: the submit button waits for the request', async () => {
  let release!: () => void
  const gate = new Promise<void>((r) => (release = r))
  const { f, user } = renderSetup(async () => {
    await gate
    return { status: 201, body: { csrf_token: 'c' } }
  })
  await fill(user)
  const submit = screen.getByRole('button', { name: '完成初始化' })
  await user.click(submit)
  expect(submit).toBeDisabled()
  release()
  await waitFor(() => expect(f.of('POST', '/api/v1/setup')).toHaveLength(1))
})

it('blocks the wizard when /data is not mounted', async () => {
  renderSetup(undefined, { data: false, secrets: true })
  expect(await screen.findByText(/\.\/data:\/data/)).toBeInTheDocument()
  expect(document.querySelectorAll('input, select, textarea')).toHaveLength(0)
  expect(screen.queryByRole('button', { name: '完成初始化' })).toBeNull()
})

it('blocks the wizard when /secrets is not mounted', async () => {
  renderSetup(undefined, { data: true, secrets: false })
  expect(await screen.findByText('secrets 目录没有挂载')).toBeInTheDocument()
  expect(screen.getByText(/\.\/secrets:\/secrets/)).toBeInTheDocument()
  expect(document.querySelectorAll('input, select, textarea')).toHaveLength(0)
  expect(screen.queryByRole('button', { name: '完成初始化' })).toBeNull()
})
