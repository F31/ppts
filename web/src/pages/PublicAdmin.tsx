import { useCallback, useEffect, useState } from 'react';
import { Link } from '../router';
import { useI18n } from '../i18n';
import type { ClientIdentity, PublicWork } from '../api';
import { deleteWork, listMyPublications, listReviewQueue, reviewWork, type PublicationStatus } from '../api';
import { can } from '../permissions';
import type { Role } from '../types';

const statusKey: Record<PublicationStatus, string> = {
  draft: 'public.statusDraft',
  pending: 'public.statusPending',
  approved: 'public.statusApproved',
  rejected: 'public.statusRejected'
};

// PublicAdmin 是控制台内的公开区管理：普通成员可见"我的发布"，
// admin/owner 额外可见"审核队列"（用户作品待审核列表）。
export function PublicAdmin({ identity, role }: { identity: ClientIdentity; role?: Role }) {
  const { t } = useI18n();
  const [mine, setMine] = useState<PublicWork[]>([]);
  const [queue, setQueue] = useState<PublicWork[]>([]);
  const [error, setError] = useState('');

  // B4-M1：审核队列要求 ADMIN（服务端 requireAdmin，public.go:333），能力判定统一走 permissions。
  const isAdmin = can(role, 'public.manage');

  const refresh = useCallback(() => {
    setError('');
    listMyPublications(identity, {})
      .then((page) => setMine(page.items))
      .catch(() => setError(t('public.loadFailed')));
    if (isAdmin) {
      listReviewQueue(identity, {})
        .then((page) => setQueue(page.items))
        .catch(() => {
          /* 非 admin 忽略 */
        });
    }
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

  return (
    <div className="public-admin">
      <section className="panel">
        <header className="panel-header">
          <h2>{t('public.myWorks')}</h2>
        </header>
        {error ? <div className="form-error">{error}</div> : null}
        {mine.length === 0 ? (
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
                    <Link to={`/watch/${w.id}`} className="btn btn-ghost">
                      {t('public.view')}
                    </Link>
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
          {queue.length === 0 ? (
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
