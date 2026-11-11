// The shell: a 240px white sidebar with a hairline, content centred at 1080px.
// Routing and pages arrive in later tasks.
const nav = ['概览', '任务', '运行记录', '连接与密钥', '快照与恢复', '设置']

export function App() {
  return (
    <div className="flex min-h-screen bg-canvas-soft">
      <aside className="w-60 shrink-0 border-r border-hairline bg-surface p-3">
        <div className="px-4 py-3 text-title">tern</div>
        <nav className="flex flex-col gap-1">
          {nav.map((label, i) => (
            <a
              key={label}
              href="#"
              className={`rounded-sm px-4 py-3 text-body ${i === 0 ? 'font-semibold text-primary' : 'text-ink-secondary'}`}
            >
              {label}
            </a>
          ))}
        </nav>
      </aside>
      <main className="mx-auto w-full max-w-[1080px] p-6" />
    </div>
  )
}
