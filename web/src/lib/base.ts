// Where tern is served. At the root normally; the QNAP package serves it
// below "/tern/" (QTS's proxy keeps the prefix), and the server says so in
// <meta name="tern-base" content="/tern/">. Every path the interface uses
// ("/api/v1/…", "/tasks") is written as if at the root and goes through
// here.

const BASE_RE = /^\/(?:[A-Za-z0-9._~-]+\/)*$/

// readBase is the base the page declares, always ending in "/"; "/" when
// it declares none or something unusable.
export function readBase(doc: Document = document): string {
  const v = doc.querySelector('meta[name="tern-base"]')?.getAttribute('content') ?? ''
  return BASE_RE.test(v) ? v : '/'
}

let base = readBase()

// setBase replaces the base read at load time (tests).
export function setBase(b: string) {
  base = BASE_RE.test(b) ? b : '/'
}

// routerBase is the router's basename: "" at the root, "/tern" below it.
export function routerBase(): string {
  return base.slice(0, -1)
}

// url puts an absolute path of the interface below the base.
export function url(path: string): string {
  return routerBase() + path
}

// appPath is the interface's own path for a location.pathname.
export function appPath(pathname: string): string {
  const b = routerBase()
  if (b && (pathname === b || pathname.startsWith(b + '/'))) return pathname.slice(b.length) || '/'
  return pathname
}
