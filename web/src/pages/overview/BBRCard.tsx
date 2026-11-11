import { useTranslation } from 'react-i18next'
import { useSystem } from '../../api/queries'
import { Card } from '../../ui/Card'
import { CodeBlock } from '../../ui/CodeBlock'
import { StatusDot } from '../../ui/StatusDot'

export function BBRCard() {
  const { t } = useTranslation()
  const sys = useSystem()
  const bbr = sys.data?.bbr
  return (
    <Card title={t('bbr.title')}>
      {bbr ? (
        <div className="flex flex-col gap-3">
          {bbr.active ? (
            <p className="flex items-center gap-2">
              <StatusDot status="success" />
              <span>{t('bbr.active')}</span>
            </p>
          ) : bbr.supported ? (
            <>
              <p>{t('bbr.supported', { current: bbr.current })}</p>
              <CodeBlock text="net.ipv4.tcp_congestion_control: bbr" copyable />
            </>
          ) : (
            <>
              <p>{t('bbr.missing')}</p>
              <CodeBlock text="modprobe tcp_bbr" copyable />
            </>
          )}
        </div>
      ) : (
        <p className="text-caption text-ink-muted">{t('common.loading')}</p>
      )}
      <p className="mt-4 border-t border-hairline pt-3 text-caption text-ink-muted">{t('bbr.note')}</p>
    </Card>
  )
}
