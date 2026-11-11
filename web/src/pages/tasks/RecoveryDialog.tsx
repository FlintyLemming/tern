import { Download } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { downloadText } from '../../lib/download'
import { recoveryText } from '../../lib/recovery'
import { Button } from '../../ui/Button'
import { Checkbox } from '../../ui/Checkbox'
import { CodeBlock } from '../../ui/CodeBlock'
import { CopyButton } from '../../ui/CopyButton'
import { Dialog } from '../../ui/Dialog'

// RecoveryDialog stands between "save" and creating a backup task: the task
// is created only after the person confirms the repository address and
// password are kept somewhere other than this machine.
export function RecoveryDialog({
  open,
  onOpenChange,
  task,
  repository,
  password,
  onConfirm,
  pending,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  task: string
  repository: string
  password: string
  onConfirm: () => void
  pending: boolean
}) {
  const { t } = useTranslation()
  const [kept, setKept] = useState(false)
  const text = recoveryText({ task, repository, password })
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) setKept(false)
        onOpenChange(o)
      }}
      wide
      title={t('task.recovery_title')}
      description={t('task.recovery_desc')}
      footer={
        <Button variant="primary" disabled={!kept} pending={pending} onClick={onConfirm}>
          {t('task.recovery_create')}
        </Button>
      }
    >
      <div className="flex flex-col gap-1">
        <p className="text-eyebrow text-ink-secondary">{t('task.recovery_repo')}</p>
        <CodeBlock text={repository} />
      </div>
      <div className="flex flex-col gap-1">
        <p className="text-eyebrow text-ink-secondary">{t('task.recovery_password')}</p>
        <CodeBlock text={password} />
      </div>
      <div className="flex flex-wrap gap-2">
        <Button icon={<Download aria-hidden className="size-4" />} onClick={() => downloadText(`tern-recovery-${task || 'task'}.txt`, text)}>
          {t('task.recovery_download')}
        </Button>
        <CopyButton text={text} label={t('task.recovery_copy')} className="px-[14px] py-1 text-body" />
      </div>
      <Checkbox checked={kept} onChange={setKept}>
        {t('task.recovery_confirm')}
      </Checkbox>
    </Dialog>
  )
}
