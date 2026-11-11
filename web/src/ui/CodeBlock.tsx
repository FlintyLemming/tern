import { CopyButton } from './CopyButton'

export function CodeBlock({ text, copyable }: { text: string; copyable?: boolean }) {
  return (
    <div className="flex items-start gap-2">
      <pre className="min-w-0 flex-1 overflow-x-auto rounded-md border border-hairline bg-canvas-soft px-3 py-2 font-mono text-caption whitespace-pre text-ink">
        {text}
      </pre>
      {copyable ? <CopyButton text={text} /> : null}
    </div>
  )
}
