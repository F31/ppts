import { useCallback, useEffect, useState } from 'react';
import { useI18n } from '../i18n';
import { can } from '../permissions';
import { describeApiError, isNotFound, settle } from '../apiError';
import {
  createShareLink,
  inviteCollaborator,
  listCollaborators,
  listShareLinks,
  removeCollaborator,
  revokeShareLink,
  shareUrl,
  updateCollaboratorRole,
  type ClientIdentity
} from '../api';
import type { Collaborator, Role, ShareLink } from '../types';

// ShareDialog 是项目「私密分享」面板（#95）：一个面板两个 Tab，与「发布到公开作品广场」彻底分开。
//   - 邀请协作者：按邮箱邀请本租户成员，指定项目级角色（viewer/reviewer/editor/admin）。
//   - 链接分享：生成不可反推 token 的访问链接，可选访问模式 / 有效期 / 口令，可随时撤回立即失效。
// 顶部明确提示「这是私密分享，不会出现在公开作品广场」，避免与公开发布混淆。
export function ShareDialog({
  identity,
  projectId,
  projectTitle,
  role,
  onClose
}: {
  identity: ClientIdentity;
  projectId: string;
  projectTitle: string;
  role: Role | undefined;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [tab, setTab] = useState<'collaborators' | 'links'>('collaborators');
  const canManage = can(role, 'project.share');

  const [collaborators, setCollaborators] = useState<Collaborator[]>([]);
  const [links, setLinks] = useState<ShareLink[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [actionError, setActionError] = useState('');
  const [busy, setBusy] = useState(false);

  const [inviteEmail, setInviteEmail] = useState('');
  const [inviteRole, setInviteRole] = useState('viewer');
  const [linkMode, setLinkMode] = useState('view_only');
  const [linkDays, setLinkDays] = useState(0);
  const [linkPassword, setLinkPassword] = useState('');
  const [copiedToken, setCopiedToken] = useState('');

  const reload = useCallback(async () => {
    setLoading(true);
    setError('');
    const c = await settle(() => listCollaborators(identity, projectId));
    const l = await settle(() => listShareLinks(identity, projectId));
    if (c.error) {
      setError(describeApiError(c.error, t('share.loadFailed'), t));
    } else {
      setCollaborators(c.data ?? []);
    }
    if (l.error) {
      // 协作者与链接是两个独立请求：链接失败不拖垮协作者列表（与 Jobs 页 phaseCounts 同策略）。
      setLinks([]);
      if (!c.error) setError(describeApiError(l.error, t('share.loadFailed'), t));
    } else {
      setLinks(l.data ?? []);
    }
    setLoading(false);
  }, [identity, projectId, t]);

  useEffect(() => {
    void reload();
  }, [reload]);

  async function run(fn: () => Promise<unknown>, failKey: string) {
    setBusy(true);
    setActionError('');
    const res = await settle(fn);
    if (res.error) {
      // 邀请租户外邮箱是业务上的正常结果（后端 404 + 明确原因），单独给更友好的文案。
      setActionError(isNotFound(res.error) ? t('share.inviteNotFound') : describeApiError(res.error, t(failKey), t));
    } else {
      await reload();
    }
    setBusy(false);
  }

  async function copy(url: string, token: string) {
    try {
      await navigator.clipboard.writeText(url);
      setCopiedToken(token);
      window.setTimeout(() => setCopiedToken(''), 2000);
    } catch {
      setActionError(t('share.copyFailed'));
    }
  }

  return (
    <div className="modal-backdrop" role="presentation" onClick={onClose}>
      <div className="modal-card share-dialog" role="dialog" aria-modal="true" onClick={(e) => e.stopPropagation()}>
        <header>
          <div>
            <span className="eyebrow">{t('share.privateBadge')}</span>
            <h2>{t('share.title')}</h2>
            <p className="cell-sub">{projectTitle || projectId}</p>
          </div>
          <button type="button" onClick={onClose}>{t('common.close')}</button>
        </header>

        <p className="share-private-note">{t('share.privateNote')}</p>

        <div className="share-tabs">
          <button type="button" className={tab === 'collaborators' ? 'active' : ''} onClick={() => setTab('collaborators')}>
            {t('share.tabCollaborators')}
          </button>
          <button type="button" className={tab === 'links' ? 'active' : ''} onClick={() => setTab('links')}>
            {t('share.tabLinks')}
          </button>
        </div>

        {error ? <p className="form-error" role="alert">{error}</p> : null}
        {actionError ? <p className="form-error" role="alert">{actionError}</p> : null}
        {loading ? <p className="cell-sub">{t('common.loading')}</p> : null}

        {tab === 'collaborators' ? (
          <section className="share-section">
            {/* A26：协作者名单当前不改变租户内可见性（见后端 RLS 仅按租户隔离），如实说明，不留假能力。 */}
            <p className="share-private-note">{t('share.collabNote')}</p>
            {!loading && collaborators.length === 0 ? <p className="cell-sub">{t('share.noCollaborators')}</p> : null}
            <ul className="share-list">
              {collaborators.map((c) => (
                <li key={c.userId}>
                  <div className="share-who">
                    <strong>{c.username || c.fullName || c.email || c.userId}</strong>
                    <span className="member-subid">{c.userId}</span>
                    {c.email ? <span className="cell-sub">{c.email}</span> : null}
                  </div>
                  <div className="share-row-actions">
                    <select
                      value={c.role}
                      disabled={!canManage || busy}
                      aria-label={t('share.role')}
                      onChange={(e) => void run(() => updateCollaboratorRole(identity, projectId, c.userId, e.target.value), 'share.roleFailed')}
                    >
                      <option value="viewer">{t('share.roleViewer')}</option>
                      <option value="reviewer">{t('share.roleReviewer')}</option>
                      <option value="editor">{t('share.roleEditor')}</option>
                      <option value="admin">{t('share.roleAdmin')}</option>
                    </select>
                    {canManage ? (
                      <button
                        type="button"
                        className="btn-danger"
                        disabled={busy}
                        onClick={() => void run(() => removeCollaborator(identity, projectId, c.userId), 'share.removeFailed')}
                      >
                        {t('share.remove')}
                      </button>
                    ) : null}
                  </div>
                </li>
              ))}
            </ul>

            {canManage ? (
              <form
                className="share-form"
                onSubmit={(e) => {
                  e.preventDefault();
                  if (!inviteEmail.trim()) return;
                  void run(() => inviteCollaborator(identity, projectId, { email: inviteEmail.trim(), role: inviteRole }), 'share.inviteFailed')
                    .then(() => setInviteEmail(''));
                }}
              >
                <input
                  type="email"
                  value={inviteEmail}
                  placeholder={t('share.inviteEmail')}
                  onChange={(e) => setInviteEmail(e.target.value)}
                />
                <select value={inviteRole} aria-label={t('share.role')} onChange={(e) => setInviteRole(e.target.value)}>
                  <option value="viewer">{t('share.roleViewer')}</option>
                  <option value="reviewer">{t('share.roleReviewer')}</option>
                  <option value="editor">{t('share.roleEditor')}</option>
                  <option value="admin">{t('share.roleAdmin')}</option>
                </select>
                <button type="submit" className="btn btn-primary" disabled={busy || !inviteEmail.trim()}>{t('share.invite')}</button>
              </form>
            ) : (
              <p className="cell-sub">{t('share.readOnlyNote')}</p>
            )}
          </section>
        ) : (
          <section className="share-section">
            {!loading && links.length === 0 ? <p className="cell-sub">{t('share.noLinks')}</p> : null}
            <ul className="share-list">
              {links.map((l) => (
                <li key={l.id}>
                  <div className="share-who">
                    <strong>{l.revoked ? t('share.linkRevoked') : fmtExpiry(l.expiresAt, t)}</strong>
                    <span className="member-subid">{l.accessMode === 'view_and_comment' ? t('share.modeComment') : t('share.modeView')}</span>
                    {l.passwordProtected ? <span className="cell-sub">{t('share.passwordSet')}</span> : null}
                  </div>
                  <div className="share-row-actions">
                    <button type="button" onClick={() => void copy(shareUrl(l.url), l.token)}>
                      {copiedToken === l.token ? t('share.copied') : t('share.copy')}
                    </button>
                    {canManage && !l.revoked ? (
                      <button
                        type="button"
                        className="btn-danger"
                        disabled={busy}
                        onClick={() => void run(() => revokeShareLink(identity, projectId, l.id), 'share.revokeFailed')}
                      >
                        {t('share.revoke')}
                      </button>
                    ) : null}
                  </div>
                </li>
              ))}
            </ul>

            {canManage ? (
              <form
                className="share-form"
                onSubmit={(e) => {
                  e.preventDefault();
                  void run(
                    () => createShareLink(identity, projectId, {
                      accessMode: linkMode,
                      password: linkPassword || undefined,
                      expiresInDays: linkDays > 0 ? linkDays : undefined
                    }),
                    'share.createFailed'
                  ).then(() => setLinkPassword(''));
                }}
              >
                <select value={linkMode} aria-label={t('share.mode')} onChange={(e) => setLinkMode(e.target.value)}>
                  <option value="view_only">{t('share.modeView')}</option>
                  <option value="view_and_comment">{t('share.modeComment')}</option>
                </select>
                <select value={linkDays} aria-label={t('share.expiry')} onChange={(e) => setLinkDays(Number(e.target.value))}>
                  <option value={0}>{t('share.expiryNever')}</option>
                  <option value={7}>{t('share.expiry7')}</option>
                  <option value={30}>{t('share.expiry30')}</option>
                </select>
                <input
                  type="password"
                  value={linkPassword}
                  placeholder={t('share.passwordOptional')}
                  onChange={(e) => setLinkPassword(e.target.value)}
                />
                <button type="submit" className="btn btn-primary" disabled={busy}>{t('share.create')}</button>
              </form>
            ) : (
              <p className="cell-sub">{t('share.readOnlyNote')}</p>
            )}
          </section>
        )}

        <div className="modal-actions">
          <button type="button" onClick={onClose}>{t('common.close')}</button>
        </div>
      </div>
    </div>
  );
}

function fmtExpiry(expiresAt: string, t: (key: string) => string): string {
  if (!expiresAt) return t('share.expiryNever');
  const d = new Date(expiresAt);
  if (Number.isNaN(d.getTime())) return t('share.expiryNever');
  return `${t('share.expiryAt')} ${d.toLocaleDateString()}`;
}
