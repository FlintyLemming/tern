import { screen, waitFor, within } from '@testing-library/react'
import { useState } from 'react'
import { Route } from 'react-router'
import { afterEach } from 'vitest'
import type { BrowseSource } from '../api/queries'
import type { FolderListing } from '../api/types'
import { fakeFetch, type Call, type Reply } from '../test/fetch'
import { renderRoutes } from '../test/render'
import { Field } from '../ui/Field'
import { PathInput } from './FolderPicker'

afterEach(() => vi.unstubAllGlobals())

const listing = (path: string, names: string[], over: Partial<FolderListing> = {}): FolderListing => ({
  path,
  missing: false,
  truncated: false,
  entries: names.map((n) => ({ name: n, path: path === '' ? n : path === '/' ? `/${n}` : `${path}/${n}`, mount: false })),
  ...over,
})

// byPath answers a browse request from a table keyed by its path parameter.
const byPath =
  (table: Record<string, FolderListing | { status: number; body: unknown }>): Reply =>
  (c: Call) => {
    const p = new URL(c.url, 'http://x').searchParams.get('path') ?? ''
    const r = table[p]
    if (!r) return { status: 500, body: { error: { code: 'internal', message: `no listing for ${p}` } } }
    return 'status' in r && 'body' in r ? r : { body: r }
  }

function Harness({ source, initial = '' }: { source: BrowseSource | null; initial?: string }) {
  const [v, setV] = useState(initial)
  return (
    <Field label="目录">
      <PathInput value={v} onChange={setV} source={source} unavailable="先选择连接" />
    </Field>
  )
}

function renderPicker(source: BrowseSource | null, routes: Record<string, Reply | Reply[]>, initial = '') {
  const f = fakeFetch(routes)
  const r = renderRoutes(<Route path="*" element={<Harness source={source} initial={initial} />} />)
  return { f, ...r }
}

const pathsAsked = (f: ReturnType<typeof fakeFetch>, path: string) =>
  f.of('GET', path).map((c) => new URL(c.url, 'http://x').searchParams.get('path') ?? '')

it('local: opens at the start, walks into a folder and fills the field', async () => {
  const { f, user } = renderPicker(
    { kind: 'local' },
    {
      'GET /api/v1/browse': byPath({
        '': listing('/share', ['CACHEDEV1_DATA', 'Public']),
        '/share/Public': listing('/share/Public', ['photos']),
      }),
    },
  )
  await user.click(screen.getByRole('button', { name: '浏览文件夹' }))
  const dialog = await screen.findByRole('dialog', { name: '选择文件夹' })
  await user.click(await within(dialog).findByRole('button', { name: 'Public' }))
  await within(dialog).findByRole('button', { name: 'photos' })
  expect(within(dialog).getByLabelText('路径')).toHaveValue('/share/Public')
  await user.click(within(dialog).getByRole('button', { name: '选择此文件夹' }))
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  expect(screen.getByLabelText('目录')).toHaveValue('/share/Public')
  expect(pathsAsked(f, '/api/v1/browse')).toEqual(['', '/share/Public'])
})

it('local: opens at the typed value, goes up and to a typed path', async () => {
  const { f, user } = renderPicker(
    { kind: 'local' },
    {
      'GET /api/v1/browse': byPath({
        '/data/docs': listing('/data/docs', ['2024']),
        '/data': listing('/data', ['docs', 'music']),
        '/srv/x': listing('/srv/x', []),
      }),
    },
    '/data/docs/',
  )
  await user.click(screen.getByRole('button', { name: '浏览文件夹' }))
  const dialog = await screen.findByRole('dialog')
  await within(dialog).findByRole('button', { name: '2024' })
  await user.click(within(dialog).getByRole('button', { name: '上一级' }))
  await within(dialog).findByRole('button', { name: 'music' })

  const bar = within(dialog).getByLabelText('路径')
  await user.clear(bar)
  await user.type(bar, 'relative{Enter}')
  expect(within(dialog).getByText('路径要以 / 开头')).toBeInTheDocument()
  await user.clear(bar)
  await user.type(bar, '/srv//x/{Enter}')
  expect(await within(dialog).findByText('这里没有子文件夹。')).toBeInTheDocument()
  expect(pathsAsked(f, '/api/v1/browse')).toEqual(['/data/docs', '/data', '/srv/x'])
})

it('says when the folder asked for does not exist, and marks mount points', async () => {
  const { user } = renderPicker(
    { kind: 'local' },
    {
      'GET /api/v1/browse': byPath({
        '/data/new/sub': {
          ...listing('/data', []),
          missing: true,
          entries: [
            { name: 'docs', path: '/data/docs', mount: false },
            { name: 'usb', path: '/data/usb', mount: true },
          ],
        },
      }),
    },
    '/data/new/sub',
  )
  await user.click(screen.getByRole('button', { name: '浏览文件夹' }))
  const dialog = await screen.findByRole('dialog')
  expect(await within(dialog).findByText('/data/new/sub 不存在，显示的是最近的上级文件夹。')).toBeInTheDocument()
  expect(within(dialog).getByLabelText('路径')).toHaveValue('/data')
  expect(within(within(dialog).getByRole('button', { name: /usb/ })).getByText('卷')).toBeInTheDocument()
  expect(within(within(dialog).getByRole('button', { name: 'docs' })).queryByText('卷')).not.toBeInTheDocument()
})

it('shows why a remote end cannot be listed, and retries', async () => {
  const fail = { status: 502, body: { error: { code: 'unreachable', message: 'rclone lsjson failed (exit 1): connection refused' } } }
  const { user } = renderPicker(
    { kind: 'connection', id: 'c1', type: 'ssh', name: 'nas2' },
    { 'GET /api/v1/connections/c1/browse': [fail, { body: listing('', ['backup']) }] },
  )
  await user.click(screen.getByRole('button', { name: '浏览文件夹' }))
  const dialog = await screen.findByRole('dialog', { name: '选择文件夹' })
  expect(within(dialog).getByText('nas2 上的文件夹')).toBeInTheDocument()
  const alert = await within(dialog).findByRole('alert')
  expect(alert).toHaveTextContent('连不上对端')
  expect(alert).toHaveTextContent('connection refused')
  expect(within(dialog).getByRole('button', { name: '选择此文件夹' })).toBeDisabled()
  await user.click(within(alert).getByRole('button', { name: '重试' }))
  await within(dialog).findByRole('button', { name: 'backup' })
  expect(within(dialog).queryByRole('alert')).not.toBeInTheDocument()
})

it('ssh: home is ~ and cannot be chosen itself; ~/ and / shortcuts work', async () => {
  const { f, user } = renderPicker(
    { kind: 'connection', id: 'c1', type: 'ssh', name: 'nas2' },
    {
      'GET /api/v1/connections/c1/browse': byPath({
        '': listing('', ['backup']),
        backup: listing('backup', ['docs']),
        '/': listing('/', ['srv']),
      }),
    },
  )
  await user.click(screen.getByRole('button', { name: '浏览文件夹' }))
  const dialog = await screen.findByRole('dialog')
  await within(dialog).findByRole('button', { name: 'backup' })
  expect(within(dialog).getByLabelText('路径')).toHaveValue('~')
  expect(within(dialog).getByRole('button', { name: '选择此文件夹' })).toBeDisabled()
  expect(within(dialog).getByText('主目录本身不能选，请进入一个子文件夹。')).toBeInTheDocument()
  expect(within(dialog).getByRole('button', { name: '上一级' })).toBeDisabled()

  await user.click(within(dialog).getByRole('button', { name: '根目录' }))
  await within(dialog).findByRole('button', { name: 'srv' })
  const bar = within(dialog).getByLabelText('路径')
  await user.clear(bar)
  await user.type(bar, '~/backup{Enter}')
  await within(dialog).findByRole('button', { name: 'docs' })
  await user.click(within(dialog).getByRole('button', { name: '选择此文件夹' }))
  expect(screen.getByLabelText('目录')).toHaveValue('backup')
  expect(pathsAsked(f, '/api/v1/connections/c1/browse')).toEqual(['', '/', 'backup'])
})

it('webdav starts at / and keeps paths absolute', async () => {
  const { user } = renderPicker(
    { kind: 'connection', id: 'c2', type: 'webdav', name: 'dav' },
    {
      'GET /api/v1/connections/c2/browse': byPath({
        '/': listing('/', ['Photos']),
        '/Photos': listing('/Photos', []),
      }),
    },
  )
  await user.click(screen.getByRole('button', { name: '浏览文件夹' }))
  const dialog = await screen.findByRole('dialog')
  expect(within(dialog).queryByRole('button', { name: '主目录' })).not.toBeInTheDocument()
  await user.click(await within(dialog).findByRole('button', { name: 'Photos' }))
  await within(dialog).findByText('这里没有子文件夹。')
  await user.click(within(dialog).getByRole('button', { name: '选择此文件夹' }))
  expect(screen.getByLabelText('目录')).toHaveValue('/Photos')
})

it('cannot browse before there is somewhere to look', () => {
  renderPicker(null, {})
  const button = screen.getByRole('button', { name: '浏览文件夹' })
  expect(button).toBeDisabled()
  expect(button).toHaveAttribute('title', '先选择连接')
})

it('cancel leaves the field as it was', async () => {
  const { user } = renderPicker({ kind: 'local' }, { 'GET /api/v1/browse': { body: listing('/', ['data']) } }, '/typed')
  await user.click(screen.getByRole('button', { name: '浏览文件夹' }))
  const dialog = await screen.findByRole('dialog')
  await user.click(await within(dialog).findByRole('button', { name: 'data' }))
  await user.click(within(dialog).getByRole('button', { name: '取消' }))
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  expect(screen.getByLabelText('目录')).toHaveValue('/typed')
})
