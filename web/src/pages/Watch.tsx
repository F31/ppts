import { useEffect, useState } from 'react';
import { Link } from '../router';
import { useI18n } from './../i18n';
import { getShowcaseWork, getShowcaseManifest, type PublicWork } from '../api';
import { describeApiError, isNotFound, settle } from '../apiError';
import type { PlaybackManifest } from '../types';
import { Player } from '../Player';

// Watch 是匿名作品播放页：展示封面与讲解概要，并复用控制台同款 Player 播放语音讲解（B3）。
// 讲解清单来自原生 HTTP 匿名端点 GET /showcase/{publicId}/manifest（B5-M3 改用不可反推的 public_id）。
export function Watch({ publicId }: { publicId: string }) {
  const { t } = useI18n();
  const [work, setWork] = useState<PublicWork | null>(null);
  const [manifest, setManifest] = useState<PlaybackManifest | null>(null);
  const [error, setError] = useState('');
  const [audioMissing, setAudioMissing] = useState(false);
  const [manifestError, setManifestError] = useState('');

  useEffect(() => {
    let cancelled = false;
    setError('');
    setAudioMissing(false);
    setManifestError('');
    setWork(null);
    setManifest(null);

    // 两个请求的失败语义不同，必须分开处理（A26：不得用同一句"未就绪"掩盖 5xx）：
    //   作品 404   → 作品不存在（正常业务结果）；作品其他错误 → 服务异常，显式报错；
    //   清单 404   → 讲解尚未生成（正常降级为"讲解未就绪"）；清单其他错误 → 显式报错。
    void (async () => {
      let w: PublicWork;
      try {
        w = await getShowcaseWork(publicId);
      } catch (err) {
        if (!cancelled) {
          setError(isNotFound(err) ? t('public.notFound') : describeApiError(err, t('public.loadFailed'), t));
        }
        return;
      }
      if (cancelled) return;
      setWork(w);

      const m = await settle(() => getShowcaseManifest(publicId));
      if (cancelled) return;
      if (m.data && m.data.resources && m.data.resources.length > 0) {
        setManifest(m.data);
      } else if (!m.error || isNotFound(m.error)) {
        setAudioMissing(true);
      } else {
        setManifestError(describeApiError(m.error, t('public.audioFailed'), t));
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [publicId, t]);

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
        ) : manifestError ? (
          <p className="watch-note form-error" role="alert">{manifestError}</p>
        ) : audioMissing ? (
          <p className="watch-note">{t('public.audioNotReady')}</p>
        ) : (
          <p className="watch-note">{t('public.audioComingSoon')}</p>
        )}
      </div>
    </div>
  );
}
