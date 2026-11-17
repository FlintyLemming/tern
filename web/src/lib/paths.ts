// Paths as the folder picker handles them: "/"-separated, absolute or
// relative to a remote end's starting point ("" is that starting point).

// cleanPath tidies a typed path the way the server wants it: no empty or
// "." parts, ".." resolved where it can be, no trailing slash.
export function cleanPath(p: string): string {
  const abs = p.startsWith('/')
  const out: string[] = []
  for (const part of p.split('/')) {
    if (part === '' || part === '.') continue
    if (part === '..') {
      if (out.length > 0 && out[out.length - 1] !== '..') out.pop()
      else if (!abs) out.push('..')
      continue
    }
    out.push(part)
  }
  return (abs ? '/' : '') + out.join('/')
}

// parentPath is the folder p is in; null for the starting points "/" and "".
export function parentPath(p: string): string | null {
  if (p === '' || p === '/') return null
  const i = p.lastIndexOf('/')
  if (i < 0) return ''
  return i === 0 ? '/' : p.slice(0, i)
}
