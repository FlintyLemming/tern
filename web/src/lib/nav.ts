// safeNext accepts only a path on this site ("/tasks", "/runs?task=x");
// anything else (another origin, "//evil", "javascript:") becomes undefined.
export function safeNext(next: string | null | undefined): string | undefined {
  if (!next || !next.startsWith('/') || next.startsWith('//') || next.startsWith('/\\')) return undefined
  try {
    const u = new URL(next, 'http://tern.invalid')
    if (u.origin !== 'http://tern.invalid') return undefined
    return u.pathname + u.search + u.hash
  } catch {
    return undefined
  }
}

// withNext appends ?next= unless the target is the start page.
export function withNext(base: string, target: string | undefined): string {
  return target && target !== '/' ? `${base}?next=${encodeURIComponent(target)}` : base
}
