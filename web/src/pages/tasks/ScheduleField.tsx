import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ApiError } from '../../api/client'
import { errorText } from '../../api/errors'
import { previewSchedule } from '../../api/queries'
import type { SchedulePreview } from '../../api/types'
import { formatDuration, type Lang } from '../../lib/format'
import { NodeTime, useNodeZone } from '../../shell/NodeTime'
import { Field } from '../../ui/Field'
import { Input } from '../../ui/Input'
import { Select } from '../../ui/Select'
import { Switch } from '../../ui/Switch'
import { NumField, useFieldError, type Draft } from './form'

const PRESETS: [string, string][] = [
  ['manual', ''],
  ['hourly', '0 * * * *'],
  ['daily', '0 3 * * *'],
  ['weekly', '0 3 * * 0'],
]
const presetOf = (expr: string) => PRESETS.find(([, e]) => e === expr)?.[0] ?? 'custom'

export interface ScheduleState {
  invalid: boolean // the server said the expression is wrong: saving is blocked
}

// ScheduleField: preset or cron, with the next five runs in the node's zone,
// asked of the server 300 ms after typing stops.
export function ScheduleField({ d, patch, onState }: { d: Draft; patch: (p: Partial<Draft>) => void; onState: (s: ScheduleState) => void }) {
  const { t, i18n } = useTranslation()
  const zone = useNodeZone()
  const fe = useFieldError()
  const [custom, setCustom] = useState(() => presetOf(d.schedule) === 'custom')
  const [preview, setPreview] = useState<SchedulePreview | null>(null)
  const [error, setError] = useState<string>()
  const seq = useRef(0)
  const onStateRef = useRef(onState)
  onStateRef.current = onState

  useEffect(() => {
    const expr = d.schedule.trim()
    const n = ++seq.current
    if (expr === '') {
      setPreview(null)
      setError(undefined)
      onStateRef.current({ invalid: false })
      return
    }
    const id = setTimeout(() => {
      previewSchedule(expr).then(
        (p) => {
          if (n !== seq.current) return // an older answer arriving late
          setPreview(p)
          setError(undefined)
          onStateRef.current({ invalid: false })
        },
        (e: unknown) => {
          if (n !== seq.current) return
          setPreview(null)
          const code = e instanceof ApiError ? e.fields?.schedule : undefined
          setError(code ? t(`field.${code}`, { defaultValue: code }) : errorText(t, e))
          onStateRef.current({ invalid: Boolean(code) })
        },
      )
    }, 300)
    return () => clearTimeout(id)
  }, [d.schedule, t])

  const preset = custom ? 'custom' : presetOf(d.schedule)
  const autoStale = preview ? formatDuration(2 * preview.period_sec, i18n.language as Lang) : undefined

  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label={t('task.schedule')}>
          <Select
            value={preset}
            onValueChange={(v) => {
              setCustom(v === 'custom')
              const expr = PRESETS.find(([k]) => k === v)?.[1]
              if (expr !== undefined) patch({ schedule: expr })
            }}
            options={[...PRESETS.map(([k]) => ({ value: k, label: t(`task.preset_${k}`) })), { value: 'custom', label: t('task.preset_custom') }]}
          />
        </Field>
        <Field label={t('task.cron')} hint={t('task.cron_hint')} error={error ?? fe('schedule')}>
          <Input
            value={d.schedule}
            spellCheck={false}
            autoComplete="off"
            className="font-mono"
            onChange={(e) => {
              setCustom(presetOf(e.target.value) === 'custom')
              patch({ schedule: e.target.value })
            }}
          />
        </Field>
      </div>
      {preview && zone ? (
        <div>
          <p id="preview-label" className="mb-1 text-eyebrow text-ink-muted">
            {t('task.preview', { zone })}
          </p>
          <ul aria-labelledby="preview-label" className="flex flex-col gap-0.5 font-mono text-caption">
            {preview.next.map((iso) => (
              <li key={iso}>
                <NodeTime iso={iso} />
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      <label className="flex items-center gap-3">
        <Switch checked={d.catch_up} onCheckedChange={(v) => patch({ catch_up: v })} aria-label={t('task.catch_up')} />
        <span>
          {t('task.catch_up')}
          <span className="block text-caption text-ink-muted">{t('task.catch_up_hint')}</span>
        </span>
      </label>
      <div className="grid gap-4 sm:grid-cols-2">
        <NumField
          label={t('task.stale_after')}
          hint={autoStale ? t('task.stale_auto', { d: autoStale }) : t('task.stale_auto_plain')}
          error={fe('stale_after_sec')}
          value={d.stale_after_sec}
          scale={3600}
          emptyZero
          onChange={(n) => patch({ stale_after_sec: n })}
        />
        <NumField
          label={t('task.max_runtime')}
          hint={t('task.max_runtime_hint')}
          error={fe('max_runtime_sec')}
          value={d.max_runtime_sec}
          scale={60}
          emptyZero
          onChange={(n) => patch({ max_runtime_sec: n })}
        />
      </div>
    </div>
  )
}
