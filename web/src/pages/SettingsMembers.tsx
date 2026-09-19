import { useCallback, useEffect, useState } from 'react';
import { listMembers, removeMember, setMemberRole, updateMemberProfile, type ClientIdentity } from '../api';
import { useI18n } from '../i18n';
import { roleKey, type Member, type Role } from '../types';

const roles: Role[] = ['ROLE_OWNER', 'ROLE_ADMIN', 'ROLE_EDITOR', 'ROLE_REVIEWER', 'ROLE_VIEWER'];
const genders = ['male', 'female', 'other', 'unknown'] as const;

type MemberForm = {
  mode: 'new' | 'edit';
  userId: string;
  role: Role;
  username: string;
  fullName: string;
  gender: string;
  birthDate: string;
  phone: string;
};

function formatDate(s?: string): string {
  if (!s) return '—';
  const d = new Date(s);
  if (isNaN(d.getTime())) return s;
  return d.toLocaleString();
}

function displayName(m: Member): string {
  return m.username || m.email || m.userId;
}

export function SettingsMembers({ identity }: { identity: ClientIdentity }) {
  const { t } = useI18n();
  const [members, setMembers] = useState<Member[]>([]);
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
    setForm({ mode: 'new', userId: '', role: 'ROLE_EDITOR', username: '', fullName: '', gender: '', birthDate: '', phone: '' });
  };

  const startEdit = (member: Member) => {
    setError('');
    setForm({
      mode: 'edit',
      userId: member.userId,
      role: member.role,
      username: member.username ?? '',
      fullName: member.fullName ?? '',
      gender: member.gender ?? '',
      birthDate: member.birthDate ?? '',
      phone: member.phone ?? '',
    });
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
      await updateMemberProfile(identity, userId, {
        username: form.username.trim(),
        fullName: form.fullName.trim(),
        gender: form.gender,
        birthDate: form.birthDate,
        phone: form.phone.trim(),
      });
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
                title={form.mode === 'edit' ? t('members.userIdImmutable') : ''}
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
          <div className="form-row">
            <label>
              {t('members.fieldUsername')}
              <input
                value={form.username}
                placeholder={t('members.fieldUsernamePlaceholder')}
                onChange={(e) => setForm((c) => (c ? { ...c, username: e.currentTarget.value } : c))}
              />
            </label>
            <label>
              {t('members.fieldFullName')}
              <input
                value={form.fullName}
                placeholder={t('members.fieldFullNamePlaceholder')}
                onChange={(e) => setForm((c) => (c ? { ...c, fullName: e.currentTarget.value } : c))}
              />
            </label>
          </div>
          <div className="form-row">
            <label>
              {t('members.fieldGender')}
              <select
                value={form.gender}
                onChange={(e) => setForm((c) => (c ? { ...c, gender: e.currentTarget.value } : c))}
              >
                <option value="">—</option>
                {genders.map((g) => (
                  <option key={g} value={g}>{t(`members.gender.${g}`)}</option>
                ))}
              </select>
            </label>
            <label>
              {t('members.fieldBirth')}
              <input
                type="month"
                value={form.birthDate}
                onChange={(e) => setForm((c) => (c ? { ...c, birthDate: e.currentTarget.value } : c))}
              />
            </label>
            <label>
              {t('members.fieldPhone')}
              <input
                value={form.phone}
                placeholder={t('members.fieldPhonePlaceholder')}
                onChange={(e) => setForm((c) => (c ? { ...c, phone: e.currentTarget.value } : c))}
              />
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
                <th>{t('members.colName')}</th>
                <th>{t('members.colGender')}</th>
                <th>{t('members.colBirth')}</th>
                <th>{t('members.colEmail')}</th>
                <th>{t('members.colPhone')}</th>
                <th>{t('members.colCreated')}</th>
                <th className="col-actions">{t('members.colActions')}</th>
              </tr>
            </thead>
            <tbody>
              {members.map((member) => (
                <tr key={member.userId}>
                  <td>
                    <div className="member-name">{displayName(member)}</div>
                    <div className="member-subid">{member.userId}</div>
                  </td>
                  <td>{t(roleKey[member.role])}</td>
                  <td>{member.fullName || '—'}</td>
                  <td>{member.gender ? t(`members.gender.${member.gender}`) : '—'}</td>
                  <td>{member.birthDate || '—'}</td>
                  <td>{member.email || '—'}</td>
                  <td>{member.phone || '—'}</td>
                  <td>{formatDate(member.createdAt)}</td>
                  <td className="col-actions">
                    <div className="row-actions">
                      <button
                        type="button"
                        onClick={() => startEdit(member)}
                        disabled={member.role === 'ROLE_OWNER'}
                        title={member.role === 'ROLE_OWNER' ? t('members.ownerLocked') : t('members.changeRole')}
                      >
                        {t('common.edit')}
                      </button>
                      <button
                        type="button"
                        className="danger"
                        onClick={() => void onRemove(member.userId)}
                        disabled={member.role === 'ROLE_OWNER'}
                        title={member.role === 'ROLE_OWNER' ? t('members.ownerLocked') : t('members.removeHint')}
                      >
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
