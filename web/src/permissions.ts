import type { Role } from './types';

// B4-M1 权限模型（A22）：前端能力判定必须逐条镜像服务端 internal/api/roles.go 的角色门禁，
// 做到「菜单与后端一致」——不在 UI 上暴露服务端会拒绝的操作，也不隐藏服务端放行的操作。
//
// 服务端角色等级（internal/api/roles.go:14-29 roleRank，越大越权）：
//   viewer=0 < reviewer=1 < editor=2 < admin=3 < owner=4
const rank: Record<Role, number> = {
  ROLE_VIEWER: 0,
  ROLE_REVIEWER: 1,
  ROLE_EDITOR: 2,
  ROLE_ADMIN: 3,
  ROLE_OWNER: 4
};

export type Capability =
  | 'project.read'
  | 'project.create'
  | 'project.archive'
  | 'script.edit'
  | 'script.review'
  | 'narration.read'
  | 'narration.generate'
  | 'artifact.list'
  | 'export.create'
  | 'artifact.download'
  | 'job.control'
  | 'gateway.manage'
  | 'member.manage'
  | 'audit.read'
  | 'public.publish'
  | 'public.manage'
  | 'public.recall'
  | 'tenant.danger'
  | 'library.view'
  | 'artifact.delete'
  | 'project.organize'
  | 'project.share'
  | 'usage.supplierCost';

// minRole：每项能力的最低角色，逐条对齐服务端校验点。
// | capability          | 最低角色 | 服务端依据                                                        |
// |---------------------|----------|-------------------------------------------------------------------|
// | project.read        | viewer   | project.go:61 List / job.go:72 List / tenant.go:68 / tenant.go:231 |
// | project.create      | editor   | project.go:35                                                     |
// | project.archive     | admin    | project.go:82                                                     |
// | script.edit         | editor   | script.go:55、script.go:120                                       |
// | script.review       | reviewer | script.go:81、script.go:99                                        |
// | narration.read      | editor   | narration.go:339、narration.go:358                                |
// | narration.generate  | editor   | narration.go:65、narration.go:167                                 |
// | artifact.list       | editor   | artifact.go:27                                                    |
// | export.create       | editor   | export.go:39                                                      |
// | artifact.download   | viewer   | export.go:102                                                     |
// | job.control         | editor   | job.go:103、job.go:182                                            |
// | gateway.manage      | admin    | gateway.go:85                                                     |
// | member.manage       | admin    | tenant.go:114、tenant.go:142                                      |
// | audit.read          | admin    | tenant.go:326、tenant.go:370                                      |
// | public.publish      | viewer   | public.go:37（POST /public/works 仅要求已认证）                     |
// | public.manage       | admin    | public.go:333（requireAdmin：featured/review/delete）              |
// | public.recall       | admin    | public.go recall 复用 requireAdmin（owner/admin 均放行，B5-M3 C-8） |
// | tenant.danger       | owner    | tenant.go:388、tenant.go:414（requireOwner）                      |
// | library.view        | owner    | artifact.go GET /artifacts requireRole RoleOwner（B5-M2 跨项目成品库）|
export const minRole: Record<Capability, Role> = {
  'project.read': 'ROLE_VIEWER',
  'project.create': 'ROLE_EDITOR',
  'project.archive': 'ROLE_ADMIN',
  'script.edit': 'ROLE_EDITOR',
  'script.review': 'ROLE_REVIEWER',
  'narration.read': 'ROLE_EDITOR',
  'narration.generate': 'ROLE_EDITOR',
  'artifact.list': 'ROLE_EDITOR',
  'export.create': 'ROLE_EDITOR',
  'artifact.download': 'ROLE_VIEWER',
  'job.control': 'ROLE_EDITOR',
  'gateway.manage': 'ROLE_ADMIN',
  'member.manage': 'ROLE_ADMIN',
  'audit.read': 'ROLE_ADMIN',
  'public.publish': 'ROLE_VIEWER',
  'public.manage': 'ROLE_ADMIN',
  'public.recall': 'ROLE_ADMIN',
  'tenant.danger': 'ROLE_OWNER',
  'library.view': 'ROLE_OWNER',
  // 成品库删除（DELETE /artifacts/{id}）与跨项目成品库曝光同门禁（owner）。
  'artifact.delete': 'ROLE_OWNER',
  'project.organize': 'ROLE_EDITOR',
  // 私密分享写入（邀请协作者 / 生成链接 / 撤回）由 collab.go 的 requireRole RoleEditor 守护；
  // 读取（看协作者列表、看链接列表）只要求已认证，与后端一致。
  'project.share': 'ROLE_EDITOR',
  // 供应商成本（采买成本/毛利口径）仅 admin/owner 可见；backend tenant.go Usage 同款门禁。
  'usage.supplierCost': 'ROLE_ADMIN'
};

export function roleRank(role: Role | undefined): number {
  return role === undefined ? -1 : rank[role];
}

// can：前端能力判定。
//
// role 为 undefined 表示「成员体系未配置或角色读取失败」。服务端 requireRole 在 members reader 为 nil 时
// 直接放行（internal/api/roles.go:33-38，开发/私有化部署），因此前端此时不隐藏，保持菜单与后端一致。
// 角色已解析时按等级严格比较——这正是 A22「Viewer/Reviewer 不能通过直接 URL 或请求越权修改」的前端半边
// （另一半由服务端 requireRole 兜底）。
export function can(role: Role | undefined, capability: Capability): boolean {
  if (role === undefined) return true;
  return roleRank(role) >= rank[minRole[capability]];
}
