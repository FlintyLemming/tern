import { screen, waitFor } from '@testing-library/react'
import { Route } from 'react-router'
import { afterEach } from 'vitest'
import { fakeFetch } from '../../test/fetch'
import { renderRoutes } from '../../test/render'
import { SetupPage } from './SetupPage'

afterEach(() => vi.unstubAllGlobals())

const good = 'correct horse battery'

function renderSetup(reply: Parameters<typeof fakeFetch>[0][string] = { status: 201, body: { csrf_token: 'c' } }) {
  const f = fakeFetch({ 'POST /api/v1/setup': reply })
  const r = renderRoutes(<Route path="/setup" element={<SetupPage />} />, '/setup')
  return { f, ...r }
}

async function fillStep1(user: ReturnType<typeof renderSetup>['user'], pw = good, again = pw, node = 'nas-home') {
  await user.type(screen.getByLabelText('管理员密码'), pw)
  await user.type(screen.getByLabelText('再输一遍'), again)
  await user.clear(screen.getByLabelText('节点名'))
  await user.type(screen.getByLabelText('节点名'), node)
  await user.click(screen.getByRole('button', { name: '下一步' }))
}

it('setup: rejects short or mismatched passwords locally', async () => {
  const { f, user } = renderSetup()
  await fillStep1(user, '123456789')
  expect(screen.getByText('至少 10 个字符')).toBeInTheDocument()
  await user.clear(screen.getByLabelText('管理员密码'))
  await user.clear(screen.getByLabelText('再输一遍'))
  await user.type(screen.getByLabelText('管理员密码'), good)
  await user.type(screen.getByLabelText('再输一遍'), good + 'x')
  await user.click(screen.getByRole('button', { name: '下一步' }))
  expect(screen.getByText('两次输入不一致')).toBeInTheDocument()
  expect(f.calls).toHaveLength(0)
})

it('setup: rejects a bad node name locally', async () => {
  const { f, user } = renderSetup()
  await fillStep1(user, good, good, 'NAS Home')
  expect(screen.getByText(/小写字母/, { selector: '.text-danger' })).toBeInTheDocument()
  expect(f.calls).toHaveLength(0)
})

it('setup: passphrase fields appear only in passphrase mode', async () => {
  const { user } = renderSetup()
  await fillStep1(user)
  expect(screen.getByRole('radio', { name: /密钥文件/ })).toBeChecked()
  expect(screen.queryByLabelText('口令')).toBeNull()
  expect(screen.getByText('重启后自动解锁，计划任务照常运行。secrets 目录要和 data 分开保管。')).toBeInTheDocument()
  await user.click(screen.getByRole('radio', { name: /^口令/ }))
  expect(screen.getByLabelText('口令')).toHaveAttribute('type', 'password')
  expect(screen.getByLabelText('再输一遍口令')).toBeInTheDocument()
  expect(screen.getByText('更安全，但每次重启后要在界面输入口令，解锁前计划任务不会运行。')).toBeInTheDocument()
  await user.click(screen.getByRole('radio', { name: /密钥文件/ }))
  expect(screen.queryByLabelText('口令')).toBeNull()
})

it('setup: submits the expected body', async () => {
  const { f, user } = renderSetup()
  await fillStep1(user)
  await user.click(screen.getByRole('button', { name: '下一步' }))
  await user.click(screen.getByRole('button', { name: '完成初始化' }))
  await waitFor(() => expect(f.of('POST', '/api/v1/setup')).toHaveLength(1))
  expect(f.calls[0].body).toEqual({ password: good, node_name: 'nas-home', unlock_mode: 'keyfile' })
})

it('setup: passphrase mode sends the passphrase', async () => {
  const { f, user } = renderSetup()
  await fillStep1(user)
  await user.click(screen.getByRole('radio', { name: /^口令/ }))
  await user.type(screen.getByLabelText('口令'), 'another long secret')
  await user.type(screen.getByLabelText('再输一遍口令'), 'another long secret')
  await user.click(screen.getByRole('button', { name: '下一步' }))
  await user.click(screen.getByRole('button', { name: '完成初始化' }))
  await waitFor(() => expect(f.calls).toHaveLength(1))
  expect(f.calls[0].body).toEqual({ password: good, node_name: 'nas-home', unlock_mode: 'passphrase', passphrase: 'another long secret' })
})

it('setup: passphrase mode checks the passphrase locally', async () => {
  const { f, user } = renderSetup()
  await fillStep1(user)
  await user.click(screen.getByRole('radio', { name: /^口令/ }))
  await user.type(screen.getByLabelText('口令'), 'short')
  await user.type(screen.getByLabelText('再输一遍口令'), 'short')
  await user.click(screen.getByRole('button', { name: '下一步' }))
  expect(screen.getByText('至少 10 个字符')).toBeInTheDocument()
  expect(f.calls).toHaveLength(0)
})

it('setup: shows server field errors', async () => {
  const { user } = renderSetup({ status: 400, body: { error: { code: 'validation', message: 'invalid setup request', fields: { node_name: 'invalid' } } } })
  await fillStep1(user)
  await user.click(screen.getByRole('button', { name: '下一步' }))
  await user.click(screen.getByRole('button', { name: '完成初始化' }))
  // back on the step with the field, the error right under it
  const node = await screen.findByLabelText('节点名')
  expect(node).toHaveAttribute('aria-invalid', 'true')
  const err = document.getElementById(node.getAttribute('aria-describedby')!.split(' ')[0])
  expect(err).toHaveTextContent('格式不对')
})

it('setup: clears secrets from state after success', async () => {
  const { f, user, qc } = renderSetup()
  await fillStep1(user)
  await user.click(screen.getByRole('button', { name: '下一步' }))
  await user.click(screen.getByRole('button', { name: '完成初始化' }))
  await waitFor(() => expect(f.calls).toHaveLength(1))
  await waitFor(() => expect(qc.getQueryData(['status'])).toMatchObject({ setup_required: false, authenticated: true }))
  await user.click(screen.getByRole('button', { name: '上一步' }))
  await user.click(screen.getByRole('button', { name: '上一步' }))
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
  await fillStep1(user)
  await user.click(screen.getByRole('button', { name: '下一步' }))
  const submit = screen.getByRole('button', { name: '完成初始化' })
  await user.click(submit)
  await user.click(submit)
  expect(submit).toBeDisabled()
  release()
  await waitFor(() => expect(f.calls).toHaveLength(1))
})
