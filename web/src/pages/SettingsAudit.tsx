import type { ClientIdentity } from '../api';
import { AuditPanel } from '../AuditPanel';
import { useI18n } from '../i18n';

export function SettingsAudit({ identity }: { identity: ClientIdentity }) {
  const { t } = useI18n();
  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">{t('settingsAudit.eyebrow')}</span>
          <h1>{t('settingsAudit.title')}</h1>
          <small className="page-sub">{t('settingsAudit.subtitle')}</small>
        </div>
      </section>
      <AuditPanel identity={identity} />
    </div>
  );
}
