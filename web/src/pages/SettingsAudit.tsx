import type { ClientIdentity } from '../api';
import { AuditPanel } from '../AuditPanel';

export function SettingsAudit({ identity }: { identity: ClientIdentity }) {
  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">设置 · 审计日志</span>
          <h1>审计日志</h1>
          <small className="page-sub">记录任务取消/重试、保留清理删除、模型网关变更等操作；管理员可见。</small>
        </div>
      </section>
      <AuditPanel identity={identity} />
    </div>
  );
}