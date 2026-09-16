import { useCallback, useEffect, useState } from 'react';
import { Link } from '../router';
import { useI18n } from '../i18n';
import type { ClientIdentity, PublicWork } from '../api';
import { deleteWork, listMyPublications, listReviewQueue, recallWork, reviewWork, type PublicationStatus } from '../api';
import { describeApiError } from '../apiError';
import { can } from '../permissions';
import type { Role } from '../types';

const statusKey: Record<PublicationStatus, string> = {
  draft: 'public.statusDraft',
  pending: 'public.statusPending',
  approved: 'public.statusApproved',
  rejected: 'public.statusRejected',
  withdrawn: 'public.statusWithdrawn'
};

// PublicAdmin 是控制台内的公开区管理：普通成员可见"我的发布"，
// admin/owner 额外可见"审核队列"（用户作品待审核列表）。
export function PublicAdmin({ identity, role }: { identity: ClientIdentity; role?: Role }) {
  const { t } = useI18n();
  const [mine, setMine] = useState<PublicWork[]>([]);
  const [queue, setQueue] = useState<PublicWork[]>([]);
  const [error, setError] = useState('');
  const [queueError, setQueueError] = useState('');
  const [mineLoading, setMineLoading] = useState(true);
  const [queueLoading, setQueueLoading] = useState(false);

  // B4-M1：审核队列要求 ADMIN（服务端 requireAdmin，public.go）。能力判定统一走 permissions。
  const isAdmin = can(role, 'public.manage');
  // B5-M3：撤回要求 owner/admin（服务端 recall 复用 requireAdmin，C-8）。
  const canRecall = can(role, 'public.recall');

  // A26：两个列表都必须如实反映接口结果——加载中 / 失败（含原因＋重试）/ 空数据三者可区分，
  // 绝不用"空列表"掩盖 4xx/5xx（此前审核队列的 .catch(() => {}) 正是这类假状态）。
  const refresh = useCallback(() => {
    setError('');
    setMineLoading(true);
    listMyPublications(identity, {})
      .then((page) => setMine(page.items))
      .catch((e: unknown) => {
        setMine([]);
        setError(describeApiError(e, t('public.loadFailed'), t));
      })
      .finally(() => setMineLoading(false));

    if (!isAdmin) {
      setQueue([]);
      return;
    }
    setQueueError('');
    setQueueLoading(true);
    listReviewQueue(identity, {})
      .then((page) => setQueue(page.items))
      .catch((e: unknown) => {
        setQueue([]);
        setQueueError(describeApiError(e, t('public.queueLoadFailed'), t));
      })
      .finally(() => setQueueLoading(false));
  }, [identity, isAdmin, t]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const onReview = async (id: string, approve: boolean) => {
    try {
      await reviewWork(identity, id, approve);
      refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('public.reviewFailed'));
    }
  };

  const onDelete = async (id: string) => {
    try {
      await deleteWork(identity, id);
      refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('public.deleteFailed'));
    }
  };

  // onRecall 由 owner/admin 撤回已发布作品：置 withdrawn 立即失效（含匿名读），B5-M3。
  const onRecall = async (publicId: string) => {
    try {
      await recallWork(identity, publicId);
      refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('public.recallFailed'));
    }
  };

  return (
    <div className="public-admin">
      <section className="panel">
        <header className="panel-header">
          <h2>{t('public.myWorks')}</h2>
          <button type="button" className="button-ghost" disabled={mineLoading} onClick={() => refresh()}>
            {mineLoading ? t('common.loading') : t('common.refresh')}
          </button>
        </header>
        {error ? (
          <div className="load-failure" role="alert">
            <p className="form-error">{error}</p>
            <button type="button" onClick={() => refresh()}>{t('common.retry')}</button>
          </div>
        ) : null}
        {error ? null : mineLoading ? (
          <p className="muted">{t('common.loading')}</p>
        ) : mine.length === 0 ? (
          <p className="muted">{t('public.noMyWorks')}</p>
        ) : (
          <ul className="pub-list">
            {mine.map((w) => (
              <li key={w.id} className="pub-item">
                <div className="pub-item-main">
                  <span className="work-kind">{w.kind === 'featured' ? t('public.kindFeatured') : t('public.kindUser')}</span>
                  <strong>{w.title}</strong>
                  <span className={`status-badge status-${w.status}`}>{t(statusKey[w.status])}</span>
                </div>
                <div className="pub-item-actions">
                  {w.status === 'approved' ? (
                    <Link to={`/watch/${w.public_id}`} className="btn btn-ghost">
                      {t('public.view')}
                    </Link>
                  ) : null}
                  {w.status === 'approved' && canRecall ? (
                    <button type="button" className="btn btn-warning" onClick={() => onRecall(w.public_id ?? w.id)}>
                      {t('public.recall')}
                    </button>
                  ) : null}
                  <button type="button" className="btn btn-danger" onClick={() => onDelete(w.id)}>
                    {t('public.delete')}
                  </button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>

      {isAdmin ? (
        <section className="panel">
          <header className="panel-header">
            <h2>{t('public.reviewQueue')}</h2>
            <span className="muted">{t('public.reviewQueueHint')}</span>
          </header>
          {queueError ? (
            <div className="load-failure" role="alert">
              <p className="form-error">{queueError}</p>
              <button type="button" onClick={() => refresh()}>{t('common.retry')}</button>
            </div>
          ) : queueLoading ? (
            <p className="muted">{t('common.loading')}</p>
          ) : queue.length === 0 ? (
            <p className="muted">{t('public.queueEmpty')}</p>
          ) : (
            <ul className="pub-list">
              {queue.map((w) => (
                <li key={w.id} className="pub-item">
                  <div className="pub-item-main">
                    <span className="work-kind">{w.kind === 'featured' ? t('public.kindFeatured') : t('public.kindUser')}</span>
                    <strong>{w.title}</strong>
                    <span className="muted">{w.project_id}</span>
                  </div>
                  <div className="pub-item-actions">
                    <button type="button" className="btn btn-primary" onClick={() => onReview(w.id, true)}>
                      {t('public.approve')}
                    </button>
                    <button type="button" className="btn btn-danger" onClick={() => onReview(w.id, false)}>
                      {t('public.reject')}
                    </button>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </section>
      ) : null}
    </div>
  );
}
