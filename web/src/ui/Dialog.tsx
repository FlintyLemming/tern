import * as RD from '@radix-ui/react-dialog'
import { X } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { cx } from '../lib/cx'

export interface DialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: ReactNode
  description?: ReactNode
  children?: ReactNode
  footer?: ReactNode
  wide?: boolean
}

// A modal: 16px radius, Level 2 shadow.
export function Dialog({ open, onOpenChange, title, description, children, footer, wide }: DialogProps) {
  const { t } = useTranslation()
  return (
    <RD.Root open={open} onOpenChange={onOpenChange}>
      <RD.Portal>
        <RD.Overlay className="fixed inset-0 z-40 bg-ink/20" />
        <RD.Content
          // without a description Radix wants an explicit "none"
          {...(description ? {} : { 'aria-describedby': undefined })}
          className={cx(
            'fixed top-1/2 left-1/2 z-50 max-h-[85vh] w-[calc(100vw-32px)] -translate-x-1/2 -translate-y-1/2 overflow-y-auto',
            'rounded-xl bg-surface p-6 shadow-2 focus:outline-none',
            wide ? 'max-w-2xl' : 'max-w-lg',
          )}
        >
          <div className="mb-4 flex items-start justify-between gap-4">
            <RD.Title className="text-title">{title}</RD.Title>
            <RD.Close aria-label={t('common.close')} className="rounded-sm p-1 text-ink-muted hover:bg-canvas-soft">
              <X aria-hidden className="size-4" />
            </RD.Close>
          </div>
          {description ? <RD.Description className="mb-4 text-caption text-ink-muted">{description}</RD.Description> : null}
          <div className="flex flex-col gap-4">{children}</div>
          {footer ? <div className="mt-6 flex flex-wrap justify-end gap-2">{footer}</div> : null}
        </RD.Content>
      </RD.Portal>
    </RD.Root>
  )
}
