import type { QueryClient } from '@tanstack/react-query'
import { qk } from './queries'
import type { Progress, Run } from './types'

const URL = '/api/v1/events'
const RETRY_MIN = 2_000
const RETRY_MAX = 30_000

// connectEvents keeps the cache honest from the server's event stream. Events
// never carry state of their own: run.updated and task.updated mark queries
// stale, run.progress writes the one thing that has no endpoint of its own.
// After any break in the stream every query is refetched, since events may
// have been missed — a run must not stay "running" on screen after it ended.
export function connectEvents(qc: QueryClient, make: (url: string) => EventSource = (u) => new EventSource(u)): () => void {
  let es: EventSource | null = null
  let broken = false
  let stopped = false
  let timer: ReturnType<typeof setTimeout> | undefined
  let delay = RETRY_MIN

  const parse = <T,>(e: Event): T | null => {
    try {
      return JSON.parse((e as MessageEvent).data) as T
    } catch {
      return null
    }
  }

  const open = () => {
    if (stopped) return
    const s = make(URL)
    es = s
    s.addEventListener('run.updated', (e) => {
      const run = parse<Run>(e)
      void qc.invalidateQueries({ queryKey: qk.overview })
      void qc.invalidateQueries({ queryKey: qk.runsAll })
      if (run?.id) void qc.invalidateQueries({ queryKey: qk.run(run.id) })
    })
    s.addEventListener('run.progress', (e) => {
      const p = parse<{ run_id: string; progress: Progress }>(e)
      if (p?.run_id) qc.setQueryData(qk.progress(p.run_id), p.progress)
    })
    s.addEventListener('task.updated', (e) => {
      const ref = parse<{ task_id: string }>(e)
      void qc.invalidateQueries({ queryKey: qk.tasks })
      void qc.invalidateQueries({ queryKey: qk.overview })
      if (ref?.task_id) void qc.invalidateQueries({ queryKey: qk.task(ref.task_id) })
    })
    s.onopen = () => {
      delay = RETRY_MIN
      if (broken) {
        broken = false
        void qc.invalidateQueries()
      }
    }
    s.onerror = () => {
      broken = true
      // The browser retries a dropped stream by itself, but gives up for good
      // on an error status (401, 503 while locked): then it is up to us.
      if (s.readyState === 2 /* CLOSED */) {
        s.close()
        clearTimeout(timer)
        timer = setTimeout(open, delay)
        delay = Math.min(delay * 2, RETRY_MAX)
      }
    }
  }

  open()
  return () => {
    stopped = true
    clearTimeout(timer)
    es?.close()
  }
}
