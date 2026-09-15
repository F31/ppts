import { useEffect, useState } from 'react';
import { Link } from '../router';
import { useI18n } from './../i18n';
import { getPublicWork, getPublicManifest, type PublicWork, type PlaybackManifest } from '../api';
import { Player } from '../Player';

// Watch 是匿名作品播放页：展示封面与讲解概要，并复用控制台同款 Player 播放语音讲解（B3）。
// 讲解清单来自原生 HTTP 匿名端点 GET /public/works/{id}/manifest，与 PlaybackManifest 同构。
export function Watch({ id }: { id: string }) {
  const { t } = useI18n();
  const [work, setWork] = useState<PublicWork | null>(null);
  const [manifest, setManifest] = useState<PlaybackManifest | null>(null);
  const [error, setError] = useState('');
  const [audioMissing, setAudioMissing] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setError('');
    setAudioMissing(false);
    setWork(null);
    setManifest(null);
    // 作品信息与播放清单并行拉取；清单缺失（narration 未就绪/404）不影响作品信息展示。
    Promise.all([getPublicWork(id), getPublicManifest(id).catch(() => null)])
      .then(([w, m]) => {
        if (cancelled) return;
        setWork(w);
        if (m && m.resources && m.resources.length > 0) {
          setManifest(m);
        } else {
          setAudioMissing(true);
        }
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
        {manifest ? (
          <Player manifest={manifest} />
        ) : audioMissing ? (
          <p className="watch-note">{t('public.audioNotReady')}</p>
        ) : (
          <p className="watch-note">{t('public.audioComingSoon')}</p>
        )}
      </div>
    </div>
  );
}
