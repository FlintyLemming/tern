import { useTranslation } from 'react-i18next'
import type { Connection, Hook } from '../../api/types'
import { Checkbox } from '../../ui/Checkbox'
import { Field } from '../../ui/Field'
import { Select } from '../../ui/Select'
import { Textarea } from '../../ui/Textarea'
import { NumField, useFieldError } from './form'

// HookFields edits the pre- or post-task command. Only the pre-task command
// can be told to ignore its failure.
export function HookFields({ which, hook, onChange, sshConns }: { which: 'pre' | 'post'; hook: Hook; onChange: (h: Hook) => void; sshConns: Connection[] }) {
  const { t } = useTranslation()
  const fe = useFieldError()
  const set = (p: Partial<Hook>) => onChange({ ...hook, ...p })
  return (
    <fieldset className="flex flex-col gap-4">
      <legend className="mb-2 text-body font-semibold">{t(which === 'pre' ? 'task.section_pre' : 'task.section_post')}</legend>
      <Field label={t('task.hook_script')} hint={t('task.hook_script_hint')} error={fe(`${which}.script`)}>
        <Textarea mono rows={4} value={hook.script} onChange={(e) => set({ script: e.target.value })} />
      </Field>
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label={t('task.hook_where')} error={fe(`${which}.where`)}>
          <Select
            value={hook.where}
            onValueChange={(v) => set({ where: v as Hook['where'], ...(v === 'local' ? { connection_id: '' } : {}) })}
            options={[
              { value: 'local', label: t('task.hook_local') },
              { value: 'ssh', label: t('task.hook_ssh') },
            ]}
          />
        </Field>
        {hook.where === 'ssh' ? (
          <Field label={t('task.hook_conn')} error={fe(`${which}.connection_id`)}>
            <Select
              value={hook.connection_id}
              onValueChange={(v) => set({ connection_id: v })}
              placeholder={t('task.conn_placeholder')}
              options={sshConns.map((c) => ({ value: c.id, label: c.name }))}
            />
          </Field>
        ) : null}
        <NumField label={t('task.hook_timeout')} error={fe(`${which}.timeout_sec`)} value={hook.timeout_sec} scale={60} onChange={(n) => set({ timeout_sec: n })} />
      </div>
      {which === 'pre' ? (
        <Checkbox checked={hook.ignore_failure} onChange={(v) => set({ ignore_failure: v })}>
          {t('task.hook_ignore')}
        </Checkbox>
      ) : null}
    </fieldset>
  )
}
