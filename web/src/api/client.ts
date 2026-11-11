// The only way the interface talks to tern: JSON over /api/v1.

export class ApiError extends Error {
  status: number
  code: string
  fields?: Record<string, string>
  retryAfter?: number

  constructor(status: number, code: string, message: string, fields?: Record<string, string>, retryAfter?: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.fields = fields
    this.retryAfter = retryAfter
  }
}

let csrf = ''

// setCsrf stores the token from setup, login or GET /session; every write sends it.
export function setCsrf(token: string): void {
  csrf = token
}

export function hasCsrf(): boolean {
  return csrf !== ''
}

let navigate: (to: string) => void = (to) => window.location.assign(to)
let onUnauthorized: () => void = () => {}

// configureClient wires the client to the router (navigate) and the query
// cache (onUnauthorized: forget that we were logged in).
export function configureClient(o: { navigate?: (to: string) => void; onUnauthorized?: () => void }) {
  if (o.navigate) navigate = o.navigate
  if (o.onUnauthorized) onUnauthorized = o.onUnauthorized
}

const PUBLIC_PATHS = ['/login', '/setup']

// loginPath is where an expired session goes, keeping the page to come back to.
export function loginPath(): string {
  const here = window.location.pathname + window.location.search
  return here === '/' ? '/login' : `/login?next=${encodeURIComponent(here)}`
}

function sessionExpired() {
  if (PUBLIC_PATHS.includes(window.location.pathname)) return
  csrf = ''
  onUnauthorized()
  navigate(loginPath())
}

export async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (method !== 'GET' && method !== 'HEAD') headers['X-CSRF-Token'] = csrf
  let payload: string | undefined
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }
  let res: Response
  try {
    res = await fetch(path, { method, headers, body: payload, credentials: 'same-origin', cache: 'no-store' })
  } catch {
    throw new ApiError(0, 'network', 'the request did not reach tern')
  }
  if (res.ok) {
    if (res.status === 204) return undefined as T
    const text = await res.text()
    return (text ? JSON.parse(text) : undefined) as T
  }
  let code = 'internal'
  let message = `HTTP ${res.status}`
  let fields: Record<string, string> | undefined
  try {
    const e = (await res.json()) as { error?: { code?: string; message?: string; fields?: Record<string, string> } }
    if (e.error?.code) code = e.error.code
    if (e.error?.message) message = e.error.message
    fields = e.error?.fields
  } catch {
    // not our error body (a proxy in between, say): keep the HTTP status
  }
  const ra = Number(res.headers.get('Retry-After'))
  const err = new ApiError(res.status, code, message, fields, Number.isFinite(ra) && ra > 0 ? ra : undefined)
  if (res.status === 401) sessionExpired()
  throw err
}
