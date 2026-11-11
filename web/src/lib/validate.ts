// Local checks that mirror the server's, so a mistake is caught before a request.

export const MIN_SECRET = 10

export function secretError(s: string): 'v.required' | 'v.too_short' | undefined {
  if (s === '') return 'v.required'
  if ([...s].length < MIN_SECRET) return 'v.too_short'
  return undefined
}

export const NODE_NAME_RE = /^[a-z0-9][a-z0-9-]{0,62}$/
