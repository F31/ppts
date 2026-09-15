import type { ClientIdentity } from '../api';
import { GatewaySettings } from '../GatewaySettings';
import { useI18n } from '../i18n';

export function SettingsModels({ identity }: { identity: ClientIdentity }) {
  const { t } = useI18n();
  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">{t('models.eyebrow')}</span>
          <h1>{t('models.title')}</h1>
          <small className="page-sub">{t('models.subtitle')}</small>
        </div>
      </section>
      <GatewaySettings identity={identity} inline />
    </div>
  );
}
