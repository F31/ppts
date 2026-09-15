import { useCallback, useEffect, useState } from 'react';
import { getNarration, getProjectSlides, type ClientIdentity } from '../api';
import { useI18n } from '../i18n';
import { Link } from '../router';

type State = {
  loading: boolean;
  slideCount: number;
  ready: boolean;
  timelineKey: string;
  pagePngCount: number;
  revisionNo: number;
  unavailReason?: string;
};

export function ProjectArtifacts({ identity, projectId }: { identity: ClientIdentity; projectId: string }) {
  const { t } = useI18n();
  const [state, setState] = useState<State>({ loading: true, slideCount: 0, ready: false, timelineKey: '', pagePngCount: 0, revisionNo: 0 });

  const load = useCallback(async () => {
    setState((current) => ({ ...current, loading: true }));
    try {
      let slideCount = 0;
      try {
        const slides = await getProjectSlides(identity, projectId);
        slideCount = slides.slides.length;
      } catch {
        // 解析未完成或不可用。
      }
      const narration = await getNarration(identity, projectId);
      setState({
        loading: false,
        slideCount,
        ready: narration.ready,
        timelineKey: narration.timelineKey,
        pagePngCount: narration.pagePngKeys?.length ?? 0,
        revisionNo: narration.revisionNo ?? 0
      });
    } catch (err) {
      setState((current) => ({ ...current, loading: false, unavailReason: err instanceof Error ? err.message : t('artifacts.loadFailed') }));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [identity, projectId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">{t('artifacts.eyebrow')}</span>
          <h1 title={projectId}>{projectId.slice(0, 14)}</h1>
          <small className="page-sub">{t('artifacts.subtitle')}</small>
        </div>
        <div className="page-actions">
          <Link to={`/projects/${projectId}/editor`} className="button-primary">
            {t('artifacts.back')}
          </Link>
          <Link to="/jobs" className="button-ghost">
            {t('artifacts.jobs')}
          </Link>
        </div>
      </section>

      {state.loading ? (
        <p className="empty-state">{t('common.loading')}</p>
      ) : (
        <>
          <section className="panel">
            <span className="eyebrow">{t('artifacts.snapshot')}</span>
            {state.ready ? (
              <dl className="detail-grid">
                <div>
                  <dt>{t('artifacts.status')}</dt>
                  <dd>
                    <span className="state-tag succeeded">{t('artifacts.voiced')}</span>
                  </dd>
                </div>
                <div>
                  <dt>{t('artifacts.pageCount')}</dt>
                  <dd>{state.slideCount}</dd>
                </div>
                <div>
                  <dt>{t('artifacts.pageRenders')}</dt>
                  <dd>{state.pagePngCount > 0 ? t('artifacts.pageCountValue', { count: state.pagePngCount }) : t('artifacts.notRendered')}</dd>
                </div>
                <div>
                  <dt>{t('artifacts.sourceRevision')}</dt>
                  <dd>rev {state.revisionNo || t('artifacts.unknown')}</dd>
                </div>
                <div>
                  <dt>{t('artifacts.timeline')}</dt>
                  <dd className="nowrap-ellipsis">{state.timelineKey}</dd>
                </div>
              </dl>
            ) : (
              <div className="empty-state first-run">
                <p>
                  {state.unavailReason
                    ? t('artifacts.unavailable', { reason: state.unavailReason })
                    : t('artifacts.none')}
                </p>
                <Link to={`/projects/${projectId}/editor`} className="button-primary">
                  {t('artifacts.goGenerate')}
                </Link>
              </div>
            )}
          </section>

          <section className="panel">
            <span className="eyebrow">{t('artifacts.exports')}</span>
            <p className="empty-state">
              {t('artifacts.exportsNote')}
            </p>
          </section>
        </>
      )}
    </div>
  );
}
