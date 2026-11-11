import { Fragment, type ReactNode } from 'react'
import { cx } from '../lib/cx'

export interface Column<T> {
  key: string
  header: ReactNode
  // render draws the cell; without it the row's field `key` is shown as text.
  render?: (row: T) => ReactNode
  // title is the full text for a rendered cell that may be cut off.
  title?: (row: T) => string | undefined
  className?: string
}

export interface TableProps<T> {
  columns: Column<T>[]
  rows: T[]
  rowKey: (row: T) => string
  // renderBelow adds a full-width row under a row (progress of a running task).
  renderBelow?: (row: T) => ReactNode
  empty?: ReactNode
  className?: string
}

export function Table<T>({ columns, rows, rowKey, renderBelow, empty, className }: TableProps<T>) {
  return (
    <div className={cx('overflow-x-auto rounded-lg border border-hairline bg-surface', className)}>
      <table className="w-full border-collapse text-left text-body">
        <thead>
          <tr className="bg-canvas-soft">
            {columns.map((c) => (
              <th key={c.key} scope="col" className={cx('px-4 py-3 text-eyebrow whitespace-nowrap text-ink-muted', c.className)}>
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 && empty ? (
            <tr>
              <td colSpan={columns.length} className="px-4 py-6 text-center text-caption text-ink-muted">
                {empty}
              </td>
            </tr>
          ) : null}
          {rows.map((row) => {
            const below = renderBelow?.(row)
            return (
              <Fragment key={rowKey(row)}>
                <tr className="border-t border-hairline">
                  {columns.map((c) => {
                    if (c.render) {
                      return (
                        <td key={c.key} className={cx('px-4 py-3 align-middle', c.className)}>
                          <div className="max-w-[28rem] truncate" title={c.title?.(row)}>
                            {c.render(row)}
                          </div>
                        </td>
                      )
                    }
                    const text = String((row as Record<string, unknown>)[c.key] ?? '')
                    return (
                      <td key={c.key} className={cx('px-4 py-3 align-middle', c.className)}>
                        <span className="block max-w-[28rem] truncate" title={text}>
                          {text}
                        </span>
                      </td>
                    )
                  })}
                </tr>
                {below ? (
                  <tr>
                    <td colSpan={columns.length} className="px-4 pb-3">
                      {below}
                    </td>
                  </tr>
                ) : null}
              </Fragment>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
