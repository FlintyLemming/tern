import { useTranslation } from 'react-i18next'
import { errorText } from '../../api/errors'
import { useCommandLine } from '../../api/queries'
import { Banner } from '../../ui/Banner'
import { CodeBlock } from '../../ui/CodeBlock'
import { Dialog } from '../../ui/Dialog'

export function CommandLineDialog({ taskId, onClose }: { taskId: string | null; onClose: () => void }) {
  const { t } = useTranslation()
  const q = useCommandLine(taskId ?? '', Boolean(taskId))
  return (
    <Dialog open={Boolean(taskId)} onOpenChange={(o) => !o && onClose()} wide title={t('task.cmd_title')} description={t('task.cmd_desc')}>
      {q.isPending ? <p className="text-caption text-ink-muted">{t('common.loading')}</p> : null}
      {q.isError ? <Banner tone="danger">{errorText(t, q.error)}</Banner> : null}
      {q.data?.lines.map((line, i) => <CodeBlock key={i} text={line} copyable />)}
    </Dialog>
  )
}
