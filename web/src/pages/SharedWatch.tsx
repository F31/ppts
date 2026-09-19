import { useEffect, useState } from 'react';
import { useI18n } from '../i18n';
import { getSharedManifest, getSharedMeta } from '../api';
import { describeApiError, isNotFound, settle } from '../apiError';
import type { PlaybackManifest, SharedMeta } from '../types';
import { Player } from '../Player';
import { PublicShell } from '../PublicShell';

// SharedWatch 是**私密分享**的匿名播放页（#95）：/shared/{token}。
// 与公开广场 /watch/{publicId} 的区别：
//   - 走独立的 /shared/* 最小字段端点，响应不含 project_id / tenant_id 等内部标识；
//   - 可能带访问口令，口令只经 X-Share-Password 请求头传递（不进 URL / 访问日志）；
//   - 链接被撤回或过期后，后端统一返回 404，本页降级为「链接无效」。
export function SharedWatch({ token }: { token: string }) {
  const { t } = useI18n();
  const [meta, setMeta] = useState<SharedMeta | null>(null);
  const [metaError, setMetaError] = useState('');
  const [password, setPassword] = useState('');
  const [submitted, setSubmitted] = useState('');
  const [manifest, setManifest] = useState<PlaybackManifest | null>(null);
  const [manifestError, setManifestError] = useState('');
  const [audioMissing, setAudioMissing] = useState(false);
  const [loadingManifest, setLoadingManifest] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setMetaError('');
    setMeta(null);
    void (async () => {
      const res = await settle(() => getSharedMeta(token));
      if (cancelled) return;
      if (res.error) {
        // 链接不存在 / 已撤回 / 已过期：后端同文案，统一按"链接无效"呈现。
        setMetaError(isNotFound(res.error) ? t('shared.invalidLink') : describeApiError(res.error, t('shared.loadFailed'), t));
        return;
      }
      setMeta(res.data ?? null);
    })();
    return () => {
      cancelled = true;
    };
  }, [token, t]);

  // 需要口令时先收口令；不需要口令或用户已提交时再去拉清单。
  useEffect(() => {
    if (!meta) return;
    if (meta.passwordProtected && !submitted) return;
    let cancelled = false;
    setLoadingManifest(true);
    setManifestError('');
    setAudioMissing(false);
    void (async () => {
      const res = await settle(() => getSharedManifest(token, submitted || undefined));
      if (cancelled) return;
      setLoadingManifest(false);
      if (res.data && res.data.resources && res.data.resources.length > 0) {
        setManifest(res.data);
      } else if (!res.error || isNotFound(res.error)) {
        // 口令错误也走同一分支（后端 404），提示用户重新输入而不是暴露"口令错"。
        setAudioMissing(true);
        if (meta.passwordProtected) {
          setSubmitted('');
          setManifestError(t('shared.audioNotReadyWithPassword'));
        }
      } else {
        setManifestError(describeApiError(res.error, t('shared.audioFailed'), t));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [meta, submitted, token, t]);

  if (metaError) {
    return (
      <PublicShell>
        <div className="watch-error">
          <p>{metaError}</p>
          <p className="cell-sub">{t('shared.invalidHint')}</p>
        </div>
      </PublicShell>
    );
  }
  if (!meta) {
    return (
      <PublicShell>
        <div className="watch-loading">{t('public.loading')}</div>
      </PublicShell>
    );
  }

  return (
    <PublicShell>
      <div className="watch">
        <div className="watch-info">
          <span className="work-kind">{t('shared.privateBadge')}</span>
          <h1>{meta.title || t('shared.untitled')}</h1>
          {meta.passwordProtected && !submitted ? (
            <form
              className="shared-password"
              onSubmit={(e) => {
                e.preventDefault();
                if (password.trim()) setSubmitted(password.trim());
              }}
            >
              <label>
                {t('shared.passwordPrompt')}
                <input
                  type="password"
                  value={password}
                  autoFocus
                  onChange={(e) => setPassword(e.target.value)}
                />
              </label>
              <button type="submit" className="btn btn-primary" disabled={!password.trim()}>
                {t('shared.unlock')}
              </button>
            </form>
          ) : manifest ? (
            <Player manifest={manifest} />
          ) : manifestError ? (
            <p className="watch-note form-error" role="alert">{manifestError}</p>
          ) : audioMissing ? (
            <p className="watch-note">{t('shared.audioNotReady')}</p>
          ) : loadingManifest ? (
            <p className="watch-note">{t('public.loading')}</p>
          ) : (
            <p className="watch-note">{t('public.audioComingSoon')}</p>
          )}
        </div>
      </div>
    </PublicShell>
  );
}
