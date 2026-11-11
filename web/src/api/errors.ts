import type { TFunction } from 'i18next'
import { ApiError } from './client'

// errorText is the localized text for an error code; an unknown code shows the
// server's English message.
export function errorText(t: TFunction, e: unknown): string {
  if (e instanceof ApiError) return t(`error.${e.code}`, { defaultValue: e.message })
  return e instanceof Error ? e.message : String(e)
}

// fieldText is the localized text for one field error code.
export function fieldText(t: TFunction, code: string | undefined): string | undefined {
  if (!code) return undefined
  return t(`field.${code}`, { defaultValue: code })
}

// fieldCodes returns the server's field → code map of a validation error, or {}.
export function fieldCodes(e: unknown): Record<string, string> {
  return e instanceof ApiError && e.fields ? e.fields : {}
}
