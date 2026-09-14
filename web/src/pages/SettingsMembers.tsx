import { useCallback, useEffect, useState } from 'react';
import { listMembers, removeMember, setMemberRole, type ClientIdentity } from '../api';
import { roleLabel, type Role } from '../types';

const roles: Role[] = ['ROLE_OWNER', 'ROLE_ADMIN', 'ROLE_EDITOR', 'ROLE_REVIEWER', 'ROLE_VIEWER'];

export function SettingsMembers({ identity }: { identity: ClientIdentity }) {
  const [members, setMembers] = useState<Array<{ userId: string; role: Role }>>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      setMembers(await listMembers(identity));
    } catch (err) {
      setError(err instanceof Error ? err.message : '成员列表加载失败');
    } finally {
      setLoading(false);
    }
  }, [identity]);

  useEffect(() => {
    void load();
  }, [load]);

  const onSetRole = async (userId: string, role: Role) => {
    setError('');
    setNotice('');
    try {
      await setMemberRole(identity, userId, role);
      setNotice(`已更新 ${userId} 的角色为「${roleLabel[role]}」。`);
      void load();
    } catch (err) {
      setError(err instanceof Error ? err.message : '更新角色失败');
    }
  };

  const onRemove = async (userId: string) => {
    if (!window.confirm(`移除成员 ${userId}？`)) return;
    setError('');
    setNotice('');
    try {
      await removeMember(identity, userId);
      setNotice(`已移除成员 ${userId}。`);
      void load();
    } catch (err) {
      setError(err instanceof Error ? err.message : '移除失败');
    }
  };

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">设置 · 成员管理</span>
          <h1>成员与权限</h1>
          <small className="page-sub">角色由服务端授权决定；操作最终以后端校验为准。</small>
        </div>
        <div className="page-actions">
          <button type="button" className="button-primary" onClick={() => void load()}>
            刷新
          </button>
        </div>
      </section>

      {error && <p className="form-error">{error}</p>}
      {notice && <p className="floating-notice">{notice}</p>}

      <section className="panel">
        <header className="table-head">
          <h2>成员</h2>
        </header>
        {loading ? (
          <p className="empty-state">加载中…</p>
        ) : members.length === 0 ? (
          <p className="empty-state">暂无成员。开发模式下成员读取可能未配置。</p>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>用户</th>
                <th>角色</th>
                <th className="col-actions">操作</th>
              </tr>
            </thead>
            <tbody>
              {members.map((member) => (
                <tr key={member.userId}>
                  <td>
                    <strong>{member.userId}</strong>
                  </td>
                  <td>
                    <select
                      value={member.role}
                      onChange={(e) => void onSetRole(member.userId, e.currentTarget.value as Role)}
                      disabled={member.role === 'ROLE_OWNER'}
                      title={member.role === 'ROLE_OWNER' ? '所有者角色不可变更' : '修改角色'}
                    >
                      {roles.map((role) => (
                        <option key={role} value={role} disabled={role === 'ROLE_OWNER'}>
                          {roleLabel[role]}
                        </option>
                      ))}
                    </select>
                  </td>
                  <td className="col-actions">
                    <div className="row-actions">
                      <button type="button" className="danger" onClick={() => void onRemove(member.userId)} disabled={member.role === 'ROLE_OWNER'}>
                        移除
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