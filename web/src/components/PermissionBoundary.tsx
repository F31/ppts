import type { ReactNode } from 'react';
import { useI18n } from '../i18n';
import { Link } from '../router';
import { can, type Capability } from '../permissions';
import type { Role } from '../types';

type Props = {
  role: Role | undefined;
  need: Capability;
  // ready：角色是否已从服务端解析完成（false 时按 pendingFallback 渲染，避免"闪一下无权页"）。
  ready?: boolean;
  // pendingFallback：角色解析中渲染的内容（默认不渲染，避免闪现）。
  pendingFallback?: ReactNode;
  // fallback：确认无权时渲染的内容（默认不渲染）。
  fallback?: ReactNode;
  children: ReactNode;
};

// PermissionBoundary：按服务端能力（角色等级，见 permissions.ts）门控 UI。
// 用于导航、按钮与路由出口，保证「菜单与后端一致」且 Viewer/Reviewer 无法通过直接 URL
// 进入越权界面（A22）。服务端 requireRole 仍是最终防线，本组件只消除"看得见但点不动"的假能力。
export function PermissionBoundary({ role, need, ready = true, pendingFallback = null, fallback = null, children }: Props) {
  if (!ready) return <>{pendingFallback}</>;
  return <>{can(role, need) ? children : fallback}</>;
}

// NoPermissionNotice：越权路由的统一占位。不渲染任何可点击的越权入口，并给出回退路径。
export function NoPermissionNotice() {
  const { t } = useI18n();
  return (
    <section className="panel no-permission" role="alert">
      <span className="eyebrow">{t('perm.eyebrow')}</span>
      <h2>{t('perm.title')}</h2>
      <p>{t('perm.body')}</p>
      <div className="perm-actions">
        <Link to="/home" className="button-ghost">
          {t('perm.backHome')}
        </Link>
      </div>
    </section>
  );
}

// PendingNotice：角色解析中的轻量占位（避免整页抖动）。
export function RolePendingNotice() {
  const { t } = useI18n();
  return <section className="panel no-permission muted-panel">{t('perm.pending')}</section>;
}
