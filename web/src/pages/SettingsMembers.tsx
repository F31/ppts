import { useCallback, useEffect, useState } from 'react';
import { listMembers, removeMember, setMemberRole, type ClientIdentity } from '../api';
import { useI18n } from '../i18n';
import { roleKey, type Role } from '../types';

const roles: Role[] = ['ROLE_OWNER', 'ROLE_ADMIN', 'ROLE_EDITOR', 'ROLE_REVIEWER', 'ROLE_VIEWER'];

type MemberForm = { mode: 'new' | 'edit'; userId: string; role: Role };

export function SettingsMembers({ identity }: { identity: ClientIdentity }) {
  const { t } = useI18n();
  const [members, setMembers] = useState<Array<{ userId: string; role: Role }>>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [form, setForm] = useState<MemberForm | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      setMembers(await listMembers(identity));
    } catch (err) {
      setError(err instanceof Error ? err.message : t('members.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [identity, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const startNew = () => {
    setError('');
    setForm({ mode: 'new', userId: '', role: 'ROLE_EDITOR' });
  };

  const startEdit = (userId: string, role: Role) => {
    setError('');
    setForm({ mode: 'edit', userId, role });
  };

  const save = async () => {
    if (!form) return;
    const userId = form.userId.trim();
    if (!userId) {
      setError(t('members.invalidUser'));
      return;
    }
    setError('');
    setNotice('');
    try {
      await setMemberRole(identity, userId, form.role);
      setNotice(form.mode === 'new'
        ? t('members.added', { user: userId, role: t(roleKey[form.role]) })
        : t('members.updated', { user: userId, role: t(roleKey[form.role]) }));
      setForm(null);
      void load();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('members.updateFailed'));
    }
  };

  const onRemove = async (userId: string) => {
    if (!window.confirm(t('members.removeConfirm', { user: userId }))) return;
    setError('');
    setNotice('');
    try {
      await removeMember(identity, userId);
      setNotice(t('members.removed', { user: userId }));
      void load();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('members.removeFailed'));
    }
  };

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">{t('members.eyebrow')}</span>
          <h1>{t('members.title')}</h1>
          <small className="page-sub">{t('members.subtitle')}</small>
        </div>
        <div className="page-actions">
          <button type="button" className="button-primary" onClick={startNew}>
            {t('members.add')}
          </button>
          <button type="button" onClick={() => void load()}>
            {t('common.refresh')}
          </button>
        </div>
      </section>

      {error && <p className="form-error">{error}</p>}
      {notice && <p className="floating-notice">{notice}</p>}

      {form && (
        <section className="panel editor-form">
          <header className="table-head">
            <h2>{form.mode === 'new' ? t('members.editorNew') : t('members.editorEdit')}</h2>
          </header>
          <div className="form-row">
            <label>
              {t('members.userId')}
              <input
                value={form.userId}
                placeholder={t('members.userIdPlaceholder')}
                disabled={form.mode === 'edit'}
                onChange={(e) => setForm((c) => (c ? { ...c, userId: e.currentTarget.value } : c))}
              />
            </label>
            <label>
              {t('members.role')}
              <select
                value={form.role}
                onChange={(e) => setForm((c) => (c ? { ...c, role: e.currentTarget.value as Role } : c))}
              >
                {roles.map((role) => (
                  <option key={role} value={role} disabled={role === 'ROLE_OWNER' && form.mode === 'new'}>
                    {t(roleKey[role])}
                  </option>
                ))}
              </select>
            </label>
          </div>
          <div className="draft-actions">
            <button type="button" className="button-primary" onClick={() => void save()}>
              {t('common.save')}
            </button>
            <button type="button" onClick={() => setForm(null)}>
              {t('common.cancel')}
            </button>
          </div>
        </section>
      )}

      <section className="panel">
        <header className="table-head">
          <h2>{t('members.listTitle')}</h2>
        </header>
        {loading ? (
          <p className="empty-state">{t('common.loading')}</p>
        ) : members.length === 0 ? (
          <p className="empty-state">{t('members.empty')}</p>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>{t('members.colUser')}</th>
                <th>{t('members.colRole')}</th>
                <th className="col-actions">{t('members.colActions')}</th>
              </tr>
            </thead>
            <tbody>
              {members.map((member) => (
                <tr key={member.userId}>
                  <td>
                    <strong>{member.userId}</strong>
                  </td>
                  <td>{t(roleKey[member.role])}</td>
                  <td className="col-actions">
                    <div className="row-actions">
                      <button
                        type="button"
                        onClick={() => startEdit(member.userId, member.role)}
                        disabled={member.role === 'ROLE_OWNER'}
                        title={member.role === 'ROLE_OWNER' ? t('members.ownerLocked') : t('members.changeRole')}
                      >
                        {t('common.edit')}
                      </button>
                      <button type="button" className="danger" onClick={() => void onRemove(member.userId)} disabled={member.role === 'ROLE_OWNER'}>
                        {t('common.remove')}
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}
