import { useEffect, useState } from 'react';
import { Link } from '../router';
import { useI18n } from '../i18n';
import { listPublicWorks, type PublicWork, type PublicationKind } from '../api';
import { describeApiError } from '../apiError';

type Tab = Extract<PublicationKind, 'featured' | 'user'>;

// Explore 是公开作品广场：双 Tab（官方精选 / 用户作品）展示已批准作品。
// 匿名可读，未登录直接可见（V1.6 公开区核心增量）。
export function Explore() {
  const { t } = useI18n();
  const [tab, setTab] = useState<Tab>('featured');
  const [works, setWorks] = useState<PublicWork[]>([]);
  const [loading, setLoading] = useState(true);
  // A26：加载失败不能伪装成"暂无作品"，否则观众会以为广场是空的。
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError('');
    listPublicWorks({ kind: tab, limit: 24 })
      .then((page) => {
        if (!cancelled) setWorks(page.items);
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setWorks([]);
          setError(describeApiError(err, t('public.loadFailed'), t));
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [tab, t]);

  return (
    <div className="explore">
      <section className="explore-hero">
        <h1>{t('public.heroTitle')}</h1>
        <p>{t('public.heroSubtitle')}</p>
      </section>

      <div className="explore-tabs" role="tablist">
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'featured'}
          className={`tab ${tab === 'featured' ? 'selected' : ''}`}
          onClick={() => setTab('featured')}
        >
          {t('public.tabFeatured')}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'user'}
          className={`tab ${tab === 'user' ? 'selected' : ''}`}
          onClick={() => setTab('user')}
        >
          {t('public.tabUser')}
        </button>
      </div>

      {loading ? (
        <div className="explore-loading">{t('public.loading')}</div>
      ) : works.length === 0 ? (
        <div className="explore-empty">{t('public.empty')}</div>
      ) : (
        <div className="work-grid">
          {works.map((w) => (
            <Link key={w.public_id ?? w.id} to={`/watch/${w.public_id ?? w.id}`} className="work-card">
              {w.cover_url ? (
                <img className="work-cover" src={w.cover_url} alt={w.title} loading="lazy" />
              ) : (
                <div className="work-cover placeholder">{t('public.noCover')}</div>
              )}
              <div className="work-meta">
                <span className="work-kind">{w.kind === 'featured' ? t('public.kindFeatured') : t('public.kindUser')}</span>
                <h3 className="work-title">{w.title}</h3>
                {w.summary ? <p className="work-summary">{w.summary}</p> : null}
              </div>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}
