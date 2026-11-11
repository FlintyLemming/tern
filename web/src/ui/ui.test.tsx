import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Button } from './Button'
import { Field } from './Field'
import { Input } from './Input'
import { StatusDot, STATUSES } from './StatusDot'
import { Table } from './Table'
import { CopyButton } from './CopyButton'
import { ProgressBar } from './ProgressBar'
import { Banner } from './Banner'
import { CodeBlock } from './CodeBlock'

// The plan's status table: colour token and Chinese label.
const statusTable: [string, string, string][] = [
  ['success', '--color-accent-green', '成功'],
  ['warning', '--color-accent-orange', '完成但有警告'],
  ['partial', '--color-accent-orange', '部分失败'],
  ['failed', '--color-danger', '失败'],
  ['interrupted', '--color-danger', '被中断'],
  ['canceled', '--color-ink-faint', '已取消'],
  ['skipped', '--color-ink-faint', '已跳过'],
  ['queued', '--color-accent-sky', '排队中'],
  ['running', '--color-accent-sky', '运行中'],
  ['retry_wait', '--color-accent-sky', '等待重试'],
]

it('StatusDot covers all ten statuses', () => {
  expect([...STATUSES].sort()).toEqual(statusTable.map((r) => r[0]).sort())
  for (const [status, color, label] of statusTable) {
    const { container, unmount } = render(<StatusDot status={status as never} withLabel />)
    const dot = container.querySelector('[data-dot]') as HTMLElement
    expect(dot.style.backgroundColor, status).toBe(`var(${color})`)
    expect(screen.getByText(label)).toBeInTheDocument()
    unmount()
  }
})

it('StatusDot without label still names the status for screen readers', () => {
  render(<StatusDot status="failed" />)
  expect(screen.getByLabelText('失败')).toBeInTheDocument()
})

it('Button pending disables and blocks clicks', async () => {
  const user = userEvent.setup()
  const onClick = vi.fn()
  render(
    <Button variant="primary" pending onClick={onClick}>
      保存
    </Button>,
  )
  const btn = screen.getByRole('button', { name: /保存/ })
  expect(btn).toBeDisabled()
  await user.click(btn)
  await user.click(btn)
  expect(onClick).not.toHaveBeenCalled()
})

it('Button primary uses pill radius', () => {
  render(
    <>
      <Button variant="primary">主</Button>
      <Button variant="utility">次</Button>
      <Button variant="danger">删</Button>
    </>,
  )
  expect(screen.getByRole('button', { name: '主' }).className).toContain('rounded-full')
  expect(screen.getByRole('button', { name: '次' }).className).toContain('rounded-md')
  expect(screen.getByRole('button', { name: '次' }).className).not.toContain('rounded-full')
  expect(screen.getByRole('button', { name: '删' }).className).toContain('bg-danger')
})

it('Button defaults to type=button so it never submits a form by accident', () => {
  render(<Button variant="utility">x</Button>)
  expect(screen.getByRole('button')).toHaveAttribute('type', 'button')
})

it('Field shows error', () => {
  render(
    <Field label="名称" error="必填">
      <Input />
    </Field>,
  )
  expect(screen.getByText('必填')).toBeVisible()
  const input = screen.getByLabelText('名称')
  expect(input).toHaveAttribute('aria-invalid', 'true')
  expect(input.className).toContain('aria-invalid:border-danger')
})

it('Field without error is not invalid', () => {
  render(
    <Field label="名称" hint="小写字母">
      <Input />
    </Field>,
  )
  expect(screen.getByLabelText('名称')).not.toHaveAttribute('aria-invalid', 'true')
  expect(screen.getByText('小写字母')).toBeInTheDocument()
})

it('Table truncates long cells with title', () => {
  const long = 'x'.repeat(300)
  render(<Table columns={[{ key: 'name', header: '名称' }]} rows={[{ id: '1', name: long }]} rowKey={(r) => r.id} />)
  const cell = screen.getByText(long)
  expect(cell).toHaveAttribute('title', long)
  expect(cell.className).toContain('truncate')
})

it('CopyButton writes to clipboard', async () => {
  const user = userEvent.setup()
  const spy = vi.spyOn(navigator.clipboard, 'writeText')
  render(<CopyButton text="ssh-ed25519 AAAA" />)
  await user.click(screen.getByRole('button'))
  expect(spy).toHaveBeenCalledWith('ssh-ed25519 AAAA')
})

it('Input never uses pill radius', () => {
  render(<Input aria-label="x" />)
  const cls = screen.getByLabelText('x').className
  expect(cls).not.toContain('rounded-full')
  expect(cls).toContain('rounded-xs')
})

it('ProgressBar reports done of total', () => {
  render(<ProgressBar done={25} total={100} />)
  const bar = screen.getByRole('progressbar')
  expect(bar).toHaveAttribute('aria-valuenow', '25')
  expect(bar).toHaveAttribute('aria-valuemax', '100')
})

it('Banner danger is an alert', () => {
  render(<Banner tone="danger">出事了</Banner>)
  expect(screen.getByRole('alert')).toHaveTextContent('出事了')
})

it('CodeBlock is monospace, scrolls sideways and can copy', () => {
  render(<CodeBlock text="restic snapshots" copyable />)
  const pre = screen.getByText('restic snapshots').closest('pre')!
  expect(pre.className).toContain('font-mono')
  expect(pre.className).toContain('overflow-x-auto')
  expect(screen.getByRole('button')).toBeInTheDocument()
})
