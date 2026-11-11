import { createContext, useContext, useId, type ReactNode } from 'react'

interface FieldCtx {
  id: string
  describedBy?: string
  invalid: boolean
}

const Ctx = createContext<FieldCtx | null>(null)

// useField lets an input pick up the label, hint and error of its Field.
export function useField() {
  return useContext(Ctx)
}

export interface FieldProps {
  label: ReactNode
  hint?: ReactNode
  error?: ReactNode
  children: ReactNode
  className?: string
}

export function Field({ label, hint, error, children, className }: FieldProps) {
  const id = useId()
  const hintId = hint ? `${id}-hint` : undefined
  const errId = error ? `${id}-err` : undefined
  const describedBy = [errId, hintId].filter(Boolean).join(' ') || undefined
  return (
    <Ctx.Provider value={{ id, describedBy, invalid: Boolean(error) }}>
      <div className={className ?? 'flex flex-col gap-1'}>
        <label htmlFor={id} className="text-eyebrow text-ink-secondary">
          {label}
        </label>
        {children}
        {error ? (
          <p id={errId} className="text-caption text-danger">
            {error}
          </p>
        ) : null}
        {hint ? (
          <p id={hintId} className="text-caption text-ink-muted">
            {hint}
          </p>
        ) : null}
      </div>
    </Ctx.Provider>
  )
}
