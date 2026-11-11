export function ProgressBar({ done, total }: { done: number; total: number }) {
  const known = total > 0
  const pct = known ? Math.min(100, Math.max(0, (done / total) * 100)) : 0
  return (
    <div
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={known ? total : undefined}
      aria-valuenow={known ? done : undefined}
      className="h-1.5 w-full overflow-hidden rounded-full bg-hairline"
    >
      <div className={known ? 'h-full bg-primary transition-[width]' : 'h-full w-1/3 animate-pulse bg-primary'} style={known ? { width: `${pct}%` } : undefined} />
    </div>
  )
}
