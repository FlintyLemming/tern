import { Check, Copy } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { cx } from '../lib/cx'

// copyText uses the clipboard API where the page is a secure context, and the
// old selection trick on plain http (a NAS on the LAN usually is).
export async function copyText(text: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return
    } catch {
      // fall through
    }
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  try {
    document.execCommand('copy')
  } finally {
    ta.remove()
  }
}

export function CopyButton({ text, label, className }: { text: string; label?: string; className?: string }) {
  const { t } = useTranslation()
  const [done, setDone] = useState(false)
  useEffect(() => {
    if (!done) return
    const id = setTimeout(() => setDone(false), 2000)
    return () => clearTimeout(id)
  }, [done])
  return (
    <button
      type="button"
      aria-label={label ?? t('common.copy')}
      onClick={() => void copyText(text).then(() => setDone(true))}
      className={cx(
        'inline-flex items-center gap-1 rounded-md border border-hairline bg-surface px-2 py-1 text-caption text-ink-secondary active:bg-canvas-soft',
        className,
      )}
    >
      {done ? <Check aria-hidden className="size-3.5" /> : <Copy aria-hidden className="size-3.5" />}
      <span>{done ? t('common.copied') : (label ?? t('common.copy'))}</span>
    </button>
  )
}
