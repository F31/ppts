import { useEffect, useState } from 'react';
import { Link } from '../router';
import { useI18n } from './../i18n';
import { getPublicWork, type PublicWork } from '../api';

// Watch 是匿名作品播放页：展示封面与讲解概要。音频播放在 B3 批次接入
//（届时复用 /ppts.v1.PlaybackService/GetManifest 的匿名化签名资源）。
export function Watch({ id }: { id: string }) {
  const { t } = useI18n();
  const [work, setWork] = useState<PublicWork | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;
    setError('');
    getPublicWork(id)
      .then((w) => {
        if (!cancelled) setWork(w);
      })
      .catch(() => {
        if (!cancelled) setError(t('public.notFound'));
      });
    return () => {
      cancelled = true;
    };
  }, [id, t]);

  if (error) {
    return (
      <div className="watch-error">
        <p>{error}</p>
        <Link to="/explore" className="btn btn-primary">
          {t('public.backToExplore')}
        </Link>
      </div>
    );
  }
  if (!work) {
    return <div className="watch-loading">{t('public.loading')}</div>;
  }

  return (
    <div className="watch">
      <Link to="/explore" className="watch-back">
        ← {t('public.backToExplore')}
      </Link>
      <div className="watch-stage">
        {work.cover_url ? (
          <img className="watch-cover" src={work.cover_url} alt={work.title} />
        ) : (
          <div className="watch-cover placeholder">{t('public.noCover')}</div>
        )}
      </div>
      <div className="watch-info">
        <span className="work-kind">{work.kind === 'featured' ? t('public.kindFeatured') : t('public.kindUser')}</span>
        <h1>{work.title}</h1>
        {work.summary ? <p className="watch-summary">{work.summary}</p> : null}
        <p className="watch-note">{t('public.audioComingSoon')}</p>
      </div>
    </div>
  );
}
