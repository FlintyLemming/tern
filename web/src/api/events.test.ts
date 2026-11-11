import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import { createElement, type ReactNode } from 'react'
import { connectEvents } from './events'
import { qk, useRunProgress } from './queries'

class FakeES {
  static all: FakeES[] = []
  static CLOSED = 2
  readyState = 0
  url: string
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  listeners = new Map<string, ((e: MessageEvent) => void)[]>()
  closed = false
  constructor(url: string) {
    this.url = url
    FakeES.all.push(this)
  }
  addEventListener(type: string, fn: (e: MessageEvent) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn])
  }
  close() {
    this.closed = true
    this.readyState = 2
  }
  emit(type: string, data: unknown) {
    for (const fn of this.listeners.get(type) ?? []) fn(new MessageEvent(type, { data: JSON.stringify(data) }))
  }
  open() {
    this.readyState = 1
    this.onopen?.()
  }
  fail(closed = false) {
    this.readyState = closed ? 2 : 0
    this.onerror?.()
  }
}

function setup() {
  FakeES.all = []
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const stop = connectEvents(qc, (url) => new FakeES(url) as unknown as EventSource)
  return { qc, stop, es: () => FakeES.all[FakeES.all.length - 1] }
}

function seed(qc: QueryClient, key: readonly unknown[]) {
  qc.setQueryData(key, { seeded: true })
  expect(qc.getQueryState(key)?.isInvalidated).toBe(false)
}

it('connects to the event stream', () => {
  const { es } = setup()
  expect(es().url).toBe('/api/v1/events')
})

it('invalidates on run.updated', () => {
  const { qc, es } = setup()
  seed(qc, qk.overview)
  seed(qc, qk.runs({}))
  seed(qc, qk.run('r1'))
  seed(qc, qk.run('r2'))
  es().emit('run.updated', { id: 'r1', task_id: 't1', status: 'success' })
  expect(qc.getQueryState(qk.overview)?.isInvalidated).toBe(true)
  expect(qc.getQueryState(qk.runs({}))?.isInvalidated).toBe(true)
  expect(qc.getQueryState(qk.run('r1'))?.isInvalidated).toBe(true)
  expect(qc.getQueryState(qk.run('r2'))?.isInvalidated).toBe(false)
})

it('invalidates tasks on task.updated', () => {
  const { qc, es } = setup()
  seed(qc, qk.tasks)
  seed(qc, qk.task('t1'))
  seed(qc, qk.overview)
  es().emit('task.updated', { task_id: 't1' })
  expect(qc.getQueryState(qk.tasks)?.isInvalidated).toBe(true)
  expect(qc.getQueryState(qk.task('t1'))?.isInvalidated).toBe(true)
  expect(qc.getQueryState(qk.overview)?.isInvalidated).toBe(true)
})

it('writes progress into cache', async () => {
  const fetchSpy = vi.fn()
  vi.stubGlobal('fetch', fetchSpy)
  const { qc, es } = setup()
  const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client: qc }, children)
  const { result } = renderHook(() => useRunProgress('r1'), { wrapper })
  expect(result.current).toBeUndefined()
  act(() => es().emit('run.progress', { run_id: 'r1', progress: { unit: 'a', bytes_done: 5, bytes_total: 10, files_done: 1, files_total: 2, speed_bps: 1, eta_sec: 5 } }))
  await waitFor(() => expect(result.current?.bytes_done).toBe(5))
  expect(fetchSpy).not.toHaveBeenCalled()
  vi.unstubAllGlobals()
})

it('invalidates everything after reconnect', () => {
  const { qc, es } = setup()
  es().open()
  seed(qc, qk.overview)
  seed(qc, qk.run('r9'))
  es().fail()
  expect(qc.getQueryState(qk.run('r9'))?.isInvalidated).toBe(false)
  es().open()
  expect(qc.getQueryState(qk.overview)?.isInvalidated).toBe(true)
  expect(qc.getQueryState(qk.run('r9'))?.isInvalidated).toBe(true)
})

it('reconnects by itself when the browser gives up on the stream', () => {
  vi.useFakeTimers()
  try {
    const { qc, es, stop } = setup()
    const first = es()
    first.open()
    seed(qc, qk.overview)
    first.fail(true) // e.g. the server answered 503 while the vault was locked
    expect(first.closed).toBe(true)
    vi.advanceTimersByTime(30_000)
    expect(FakeES.all.length).toBe(2)
    es().open()
    expect(qc.getQueryState(qk.overview)?.isInvalidated).toBe(true)
    stop()
    expect(es().closed).toBe(true)
    vi.advanceTimersByTime(60_000)
    expect(FakeES.all.length).toBe(2)
  } finally {
    vi.useRealTimers()
  }
})
