import * as RT from '@radix-ui/react-toast'
import { createContext, useCallback, useContext, useState, type ReactNode } from 'react'
import { cx } from '../lib/cx'

type Tone = 'info' | 'danger'
interface Item {
  id: number
  text: ReactNode
  tone: Tone
}

const Ctx = createContext<(text: ReactNode, tone?: Tone) => void>(() => {})

// useToast returns a function that shows a short message in the corner.
export function useToast() {
  return useContext(Ctx)
}

let next = 1

export function Toaster({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Item[]>([])
  const show = useCallback((text: ReactNode, tone: Tone = 'info') => {
    setItems((xs) => [...xs, { id: next++, text, tone }])
  }, [])
  return (
    <Ctx.Provider value={show}>
      <RT.Provider duration={5000}>
        {children}
        {items.map((it) => (
          <RT.Root
            key={it.id}
            onOpenChange={(open) => {
              if (!open) setItems((xs) => xs.filter((x) => x.id !== it.id))
            }}
            className={cx(
              'rounded-xl border bg-surface px-4 py-3 text-body shadow-2',
              it.tone === 'danger' ? 'border-danger text-danger' : 'border-hairline text-ink',
            )}
          >
            <RT.Description>{it.text}</RT.Description>
          </RT.Root>
        ))}
        <RT.Viewport className="fixed right-4 bottom-4 z-50 flex w-[calc(100vw-32px)] max-w-sm flex-col gap-2 outline-none" />
      </RT.Provider>
    </Ctx.Provider>
  )
}
