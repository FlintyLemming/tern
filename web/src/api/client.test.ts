import { afterEach, beforeEach } from 'vitest'
import { ApiError, configureClient, request, setCsrf } from './client'
import { setBase } from '../lib/base'
import { fakeFetch } from '../test/fetch'

beforeEach(() => {
  configureClient({ navigate: (to) => window.history.pushState({}, '', to), onUnauthorized: () => {} })
  window.history.pushState({}, '', '/')
})
afterEach(() => vi.unstubAllGlobals())

// fail resolves to the error a request was expected to throw.
const fail = (p: Promise<unknown>): Promise<ApiError> =>
  p.then(
    () => {
      throw new Error('the request did not fail')
    },
    (e: ApiError) => e,
  )

it('sends CSRF header on writes only', async () => {
  const f = fakeFetch({ 'GET /api/v1/tasks': { body: [] }, 'POST /api/v1/tasks': { status: 201, body: { id: 't' } } })
  setCsrf('tok')
  await request('GET', '/api/v1/tasks')
  await request('POST', '/api/v1/tasks', { name: 'x' })
  expect(f.calls[0].headers['x-csrf-token']).toBeUndefined()
  expect(f.calls[1].headers['x-csrf-token']).toBe('tok')
  expect(f.calls[1].headers['content-type']).toBe('application/json')
  expect(f.calls[1].body).toEqual({ name: 'x' })
})

it('maps error bodies to ApiError', async () => {
  fakeFetch({
    'POST /api/v1/tasks': { status: 400, body: { error: { code: 'validation', message: 'invalid task', fields: { name: 'required' } } } },
  })
  const err = await fail(request('POST', '/api/v1/tasks', {}))
  expect(err).toBeInstanceOf(ApiError)
  expect(err.status).toBe(400)
  expect(err.code).toBe('validation')
  expect(err.message).toBe('invalid task')
  expect(err.fields).toEqual({ name: 'required' })
})

it('reads Retry-After on 429', async () => {
  fakeFetch({ 'POST /api/v1/login': { status: 429, body: { error: { code: 'rate_limited', message: 'slow down' } }, headers: { 'Retry-After': '60' } } })
  window.history.pushState({}, '', '/login')
  const err = await fail(request('POST', '/api/v1/login', { password: 'x' }))
  expect(err.code).toBe('rate_limited')
  expect(err.retryAfter).toBe(60)
})

it('redirects to login on 401 with next', async () => {
  const onUnauthorized = vi.fn()
  configureClient({ navigate: (to) => window.history.pushState({}, '', to), onUnauthorized })
  fakeFetch({ 'PUT /api/v1/tasks/abc': { status: 401, body: { error: { code: 'unauthorized', message: 'login required' } } } })
  window.history.pushState({}, '', '/tasks/abc')
  const err = await fail(request('PUT', '/api/v1/tasks/abc', {}))
  expect(err.status).toBe(401) // the caller still sees the failure: nothing fails silently
  expect(window.location.pathname + window.location.search).toBe('/login?next=%2Ftasks%2Fabc')
  expect(onUnauthorized).toHaveBeenCalled()
})

it('does not redirect when already on login', async () => {
  fakeFetch({ 'POST /api/v1/login': { status: 401, body: { error: { code: 'unauthorized', message: 'wrong password' } } } })
  window.history.pushState({}, '', '/login?next=%2Ftasks')
  await request('POST', '/api/v1/login', { password: 'x' }).catch(() => {})
  expect(window.location.pathname + window.location.search).toBe('/login?next=%2Ftasks')
})

it('turns a network failure into ApiError code network', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Failed to fetch') }))
  const err = await fail(request('GET', '/api/v1/tasks'))
  expect(err).toBeInstanceOf(ApiError)
  expect(err.code).toBe('network')
})

it('returns undefined for 204', async () => {
  fakeFetch({ 'DELETE /api/v1/tasks/a': { status: 204 } })
  await expect(request('DELETE', '/api/v1/tasks/a')).resolves.toBeUndefined()
})

describe('below a base path (QNAP package)', () => {
  beforeEach(() => setBase('/tern/'))

  it('sends requests below it', async () => {
    const f = fakeFetch({ 'GET /tern/api/v1/tasks': { body: [] } })
    await request('GET', '/api/v1/tasks')
    expect(f.calls[0].path).toBe('/tern/api/v1/tasks')
  })

  it('sends an expired session to the login page with the in-app path', async () => {
    const seen: string[] = []
    configureClient({ navigate: (to) => seen.push(to), onUnauthorized: () => {} })
    fakeFetch({ 'GET /tern/api/v1/tasks/abc': { status: 401, body: { error: { code: 'unauthorized', message: 'login required' } } } })
    window.history.pushState({}, '', '/tern/tasks/abc')
    await fail(request('GET', '/api/v1/tasks/abc'))
    // the router adds the base itself
    expect(seen).toEqual(['/login?next=%2Ftasks%2Fabc'])
  })

  it('stays put on a 401 from the login page itself', async () => {
    const seen: string[] = []
    configureClient({ navigate: (to) => seen.push(to), onUnauthorized: () => {} })
    fakeFetch({ 'POST /tern/api/v1/login': { status: 401, body: { error: { code: 'unauthorized', message: 'wrong password' } } } })
    window.history.pushState({}, '', '/tern/login')
    await fail(request('POST', '/api/v1/login', { password: 'x' }))
    expect(seen).toEqual([])
  })
})
