// A fake fetch for tests: routes "METHOD /path" (query string ignored unless
// the key has one) to canned responses and records every call.
import { vi } from 'vitest'

export interface Call {
  method: string
  url: string
  path: string
  body: unknown
  headers: Record<string, string>
}

export type Reply =
  | { status?: number; body?: unknown; headers?: Record<string, string> }
  | ((call: Call) => { status?: number; body?: unknown; headers?: Record<string, string> } | Promise<{ status?: number; body?: unknown; headers?: Record<string, string> }>)

export function fakeFetch(routes: Record<string, Reply | Reply[]>) {
  const calls: Call[] = []
  const queues = new Map<string, Reply[]>()
  for (const [k, v] of Object.entries(routes)) queues.set(k, Array.isArray(v) ? [...v] : [v])
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString()
    const method = (init?.method ?? 'GET').toUpperCase()
    const path = url.split('?')[0]
    const headers = Object.fromEntries(new Headers(init?.headers).entries())
    const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined
    const call = { method, url, path, body, headers }
    calls.push(call)
    const q = queues.get(`${method} ${url}`) ?? queues.get(`${method} ${path}`)
    if (!q || q.length === 0) {
      return new Response(JSON.stringify({ error: { code: 'not_found', message: `no fake for ${method} ${url}` } }), {
        status: 404,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    const r = q.length > 1 ? q.shift()! : q[0]
    const out = typeof r === 'function' ? await r(call) : r
    const status = out.status ?? 200
    const hasBody = out.body !== undefined && status !== 204
    return new Response(hasBody ? JSON.stringify(out.body) : null, {
      status,
      headers: { 'Content-Type': 'application/json', ...(out.headers ?? {}) },
    })
  })
  vi.stubGlobal('fetch', fn)
  return {
    fn,
    calls,
    // set replaces the replies of one route
    set(key: string, reply: Reply | Reply[]) {
      queues.set(key, Array.isArray(reply) ? [...reply] : [reply])
    },
    of(method: string, path: string) {
      return calls.filter((c) => c.method === method && c.path === path)
    },
  }
}
