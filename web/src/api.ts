import type {
  ArtifactFormat,
  AuditArchiveFile,
  AuditEvent,
  Collaborator,
  Folder,
  Job,
  Member,
  PlaybackManifest,
  Project,
  ProjectOrg,
  PronunciationDictionary,
  PronunciationRule,
  ProjectUsage,
  Role,
  ScriptMode,
  ScriptRevision,
  ScriptSegment,
  SharedMeta,
  ShareLink,
  SlideSummary,
  StorageUsage,
  Tag,
  TenantPolicy,
  TenantQuota,
  TenantUsage
} from './types';

export type ClientIdentity = {
  tenantId: string;
  userId: string;
  accessToken?: string;
  // account 为登录账号（邮箱或手机号），仅邮箱注册/登录路径写入，用于界面展示；
  // 旧会话（localStorage 无此字段）回退显示 userId。
  account?: string;
  // tenantName 为租户显示名，仅邮箱注册/登录路径从后端 tenants.name 带回；
  // 旧会话或开发/OIDC 登录无此字段时，界面回退显示 tenantId。
  tenantName?: string;
  // tenantType 为租户类型（personal/organization），由邮箱注册/登录与本地模式带回。
  // 个人账号（单成员）隐藏"成员管理/邀请协作者"入口；旧会话/开发/OIDC 无此字段时
  // 按组织处理（不隐藏），与后端不按类型分叉的宽松语义一致。
  tenantType?: string;
};

export class ConnectError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = 'ConnectError';
    this.code = code;
  }
}

function identityHeaders(identity: ClientIdentity): Record<string, string> {
  if (identity.accessToken) {
    return { Authorization: `Bearer ${identity.accessToken}` };
  }
  return {
    'X-PPTS-Tenant-ID': identity.tenantId,
    'X-PPTS-User-ID': identity.userId
  };
}

// 项目讲稿语言偏好：讲稿/配音等端点按请求头 Accept-Language 选择语言。
// 编辑器设置后，所有 API 请求携带该语言，避免"一键成稿生成英文但面板仍显示中文"。
let preferredScriptLanguage: string | undefined;

export function setScriptLanguagePreference(language?: string): void {
  preferredScriptLanguage = language && language.trim() ? language.trim() : undefined;
}

// 当前查看的源版本：讲稿/来源/配音按 (源版本, slide_id) 隔离。slide_id 只在单个 PPTX
// 内唯一，改版重传会重复；编辑器设置后所有 API 请求携带该版本，读写落在正确的版本上。
let preferredSourceRevision: number | undefined;

export function setSourceRevisionPreference(revision?: number): void {
  preferredSourceRevision = revision && revision > 0 ? revision : undefined;
}

function languageHeader(): Record<string, string> {
  const headers: Record<string, string> = {};
  if (preferredScriptLanguage) {
    headers['X-PPTS-Language'] = preferredScriptLanguage;
    headers['Accept-Language'] = preferredScriptLanguage;
  }
  if (preferredSourceRevision) {
    headers['X-PPTS-Source-Revision'] = String(preferredSourceRevision);
  }
  return headers;
}

// RevisionDiff 是两次源版本之间的页级差异。
export type RevisionDiff = {
  added: Array<{ slideId: string; name: string; pageCount: number }>;
  removed: Array<{ slideId: string; name: string; pageCount: number }>;
  changed: Array<{
    slideId: string;
    oldName: string;
    newName: string;
    oldNotes: string;
    newNotes: string;
    pageCount: number;
  }>;
};

// getRevisionDiff 比较两个源版本的幻灯片差异。
export async function getRevisionDiff(
  identity: ClientIdentity,
  projectId: string,
  revA: number,
  revB: number
): Promise<RevisionDiff> {
  const r = await getJSON<{
    added: RevisionDiff['added'];
    removed: RevisionDiff['removed'];
    changed: RevisionDiff['changed'];
  }>(identity, `/projects/${encodeURIComponent(projectId)}/revisions/${revA}/diff/${revB}`);
  return r as RevisionDiff;
}


// ---- 邮箱自助注册（B5-M4）----
export type EmailAuthResult = {
  access_token: string;
  tenant_id: string;
  tenant_name: string;
  tenant_type: string;
  user_id: string;
  account: string;
};

// AuthConfig 是后端认证能力探测结果。
// local=true 表示单租户本地模式（SQLite profile）：无需登录，前端以 tenant_id/user_id
// 固定身份自动进入。email_password 控制邮箱登录入口显隐。
export type AuthConfig = {
  email_password: boolean;
  local?: boolean;
  tenant_id?: string;
  user_id?: string;
  tenant_type?: string;
  // tenant_name 仅本地模式回传（种子租户显示名），供个人信息弹窗/欢迎语展示。
  tenant_name?: string;
};

// getAuthConfig 探测后端认证能力（无认证端点）；登录页据此显隐邮箱入口，
// App 启动时据此判断是否本地模式自动进入。
export async function getAuthConfig(): Promise<AuthConfig> {
  const response = await fetch('/auth/config');
  if (!response.ok) {
    throw new ConnectError(`http_${response.status}`, `auth config failed: HTTP ${response.status}`);
  }
  return (await response.json()) as AuthConfig;
}

// registerEmail 自助注册：后端按 accountType 创建个人/组织租户并签发 JWT，无需邮件验证（决策 ②A）。
// 请求体字段名与后端 internal/api/emailauth.go 的 json tag 一一对应（显式映射，避免驼峰字段被静默丢弃）。
export async function registerEmail(params: {
  email: string;
  password: string;
  accountType: 'personal' | 'organization';
  orgName?: string;
  username?: string;
  fullName?: string;
  gender?: string;
  birthDate?: string;
  phone?: string;
}): Promise<EmailAuthResult> {
  return postAuth('/auth/register', {
    email: params.email,
    password: params.password,
    account_type: params.accountType,
    org_name: params.orgName ?? '',
    username: params.username ?? '',
    full_name: params.fullName ?? '',
    gender: params.gender ?? '',
    birth_date: params.birthDate ?? '',
    phone: params.phone ?? ''
  });
}

// loginEmail 邮箱登录：后端校验凭证并签发 JWT。
export async function loginEmail(params: { email: string; password: string }): Promise<EmailAuthResult> {
  return postAuth('/auth/email-login', params);
}

// postAuth 通用无认证 POST（注册/登录），解析后端 {code,message} 错误体。
async function postAuth(path: string, body: Record<string, unknown>): Promise<EmailAuthResult> {
  const response = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body)
  });
  if (!response.ok) {
    let code = `http_${response.status}`;
    let message = `${path} failed: HTTP ${response.status}`;
    try {
      const envelope = (await response.json()) as { code?: string; message?: string };
      if (envelope.code) code = envelope.code;
      if (envelope.message) message = envelope.message;
    } catch {
      // 非 JSON 错误体，保留默认 message。
    }
    throw new ConnectError(code, message);
  }
  return (await response.json()) as EmailAuthResult;
}

export async function getPlaybackManifest(params: {
  identity: ClientIdentity;
  projectId: string;
  timelineKey: string;
  pagePngKeys: string[];
  ttlSeconds: number;
}): Promise<PlaybackManifest> {
  const response = await fetch('/ppts.v1.PlaybackService/GetManifest', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...identityHeaders(params.identity)
    },
    body: JSON.stringify({
      projectId: params.projectId,
      timelineKey: params.timelineKey,
      pagePngKeys: params.pagePngKeys,
      ttlSeconds: params.ttlSeconds
    })
  });
  if (!response.ok) {
    throw new Error(`GetManifest failed: HTTP ${response.status}`);
  }
  return (await response.json()) as PlaybackManifest;
}

async function connectJSON<T>(identity: ClientIdentity, procedure: string, body: unknown, extraHeaders?: Record<string, string>): Promise<T> {
  const response = await fetch(procedure, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...identityHeaders(identity),
      ...languageHeader(),
      ...extraHeaders
    },
    body: JSON.stringify(body)
  });
  if (!response.ok) {
    let code = `http_${response.status}`;
    let message = `${procedure} failed: HTTP ${response.status}`;
    try {
      const envelope = (await response.json()) as { code?: string; message?: string };
      if (envelope.code) code = envelope.code;
      if (envelope.message) message = envelope.message;
    } catch {
      // 非 Connect 错误体，保留默认 message。
    }
    throw new ConnectError(code, message);
  }
  return (await response.json()) as T;
}

// getJSON 调用后端原生 HTTP GET 端点（不走 Connect RPC），用于公开区/创作辅助等无法经 proto 生成的接口。
async function getJSON<T>(identity: ClientIdentity, path: string): Promise<T> {
  const response = await fetch(path, {
    method: 'GET',
    headers: { ...identityHeaders(identity), ...languageHeader() }
  });
  if (!response.ok) {
    let code = `http_${response.status}`;
    let message = `${path} failed: HTTP ${response.status}`;
    try {
      const envelope = (await response.json()) as { code?: string; message?: string };
      if (envelope.code) code = envelope.code;
      if (envelope.message) message = envelope.message;
    } catch {
      // 非 Connect 错误体，保留默认 message。
    }
    throw new ConnectError(code, message);
  }
  return (await response.json()) as T;
}

// putJSON 调用后端原生 HTTP PUT 端点（用于来源选择等轻量原生接口）。
async function putJSON<T>(identity: ClientIdentity, path: string, body: Record<string, unknown>): Promise<T> {
  const response = await fetch(path, {
    method: 'PUT',
    headers: { ...identityHeaders(identity), ...languageHeader(), 'Content-Type': 'application/json' },
    body: JSON.stringify(body)
  });
  if (!response.ok) {
    let code = `http_${response.status}`;
    let message = `${path} failed: HTTP ${response.status}`;
    try {
      const envelope = (await response.json()) as { code?: string; message?: string };
      if (envelope.code) code = envelope.code;
      if (envelope.message) message = envelope.message;
    } catch {
      // 非 Connect 错误体，保留默认 message。
    }
    throw new ConnectError(code, message);
  }
  return (await response.json()) as T;
}

// postJSON 调用后端原生 HTTP POST 端点（标签/分组创建等）。
async function postJSON<T>(identity: ClientIdentity, path: string, body: Record<string, unknown>): Promise<T> {
  const response = await fetch(path, {
    method: 'POST',
    headers: { ...identityHeaders(identity), ...languageHeader(), 'Content-Type': 'application/json' },
    body: JSON.stringify(body)
  });
  if (!response.ok) {
    let code = `http_${response.status}`;
    let message = `${path} failed: HTTP ${response.status}`;
    try {
      const envelope = (await response.json()) as { code?: string; message?: string };
      if (envelope.code) code = envelope.code;
      if (envelope.message) message = envelope.message;
    } catch {
      /* 非 Connect 错误体，保留默认 message */
    }
    throw new ConnectError(code, message);
  }
  return (await response.json()) as T;
}

// deleteJSON 调用后端原生 HTTP DELETE 端点（标签/分组删除等）。
async function deleteJSON<T>(identity: ClientIdentity, path: string): Promise<T> {
  const response = await fetch(path, {
    method: 'DELETE',
    headers: { ...identityHeaders(identity) }
  });
  if (!response.ok) {
    let code = `http_${response.status}`;
    let message = `${path} failed: HTTP ${response.status}`;
    try {
      const envelope = (await response.json()) as { code?: string; message?: string };
      if (envelope.code) code = envelope.code;
      if (envelope.message) message = envelope.message;
    } catch {
      /* 非 Connect 错误体，保留默认 message */
    }
    throw new ConnectError(code, message);
  }
  return (await response.json()) as T;
}

export async function listProjects(identity: ClientIdentity): Promise<Project[]> {
  const data = await connectJSON<{ projects?: Project[] }>(identity, '/ppts.v1.ProjectService/List', { pageSize: 20 });
  return data.projects ?? [];
}

export type ProjectPage = { projects: Project[]; nextCursor: string };

// listProjectsPage 带游标读取项目列表。
// ListProjectsResponse 只返回 projects + next_cursor，**没有总数**（proto/ppts/v1/project.proto:68），
// 因此 nextCursor 为空时才可把本页长度当作真实总数（D0-3，A04：分页 List 的本页长度不得当总数）。
export async function listProjectsPage(
  identity: ClientIdentity,
  params: { cursor?: string; pageSize: number }
): Promise<ProjectPage> {
  const data = await connectJSON<{ projects?: Project[]; nextCursor?: { value?: string } }>(
    identity,
    '/ppts.v1.ProjectService/List',
    {
      pageSize: params.pageSize,
      ...(params.cursor ? { cursor: { value: params.cursor } } : {})
    }
  );
  return { projects: data.projects ?? [], nextCursor: data.nextCursor?.value ?? '' };
}

// getProject 按 id 获取单个项目（含 title、currentRevision 等），供详情页展示。
export async function getProject(identity: ClientIdentity, id: string): Promise<Project> {
  return connectJSON<Project>(identity, '/ppts.v1.ProjectService/Get', { id });
}

export async function createProject(identity: ClientIdentity, title: string): Promise<Project> {
  const data = await connectJSON<{ project: Project }>(identity, '/ppts.v1.ProjectService/Create', { title });
  return data.project;
}

export async function archiveProject(identity: ClientIdentity, id: string): Promise<Project> {
  return connectJSON<Project>(identity, '/ppts.v1.ProjectService/Archive', { id });
}

export async function getProjectSlides(
  identity: ClientIdentity,
  projectId: string,
  revisionNo?: number
): Promise<{ revisionNo: number; slides: SlideSummary[] }> {
  const data = await connectJSON<{ revisionNo: number | string; slides?: SlideSummary[] }>(
    identity,
    '/ppts.v1.ProjectService/GetSlides',
    revisionNo && revisionNo > 0 ? { projectId, revisionNo } : { projectId }
  );
  // protojson 把 int64 序列化为字符串；这里统一归一为 number，避免与 REST 端点的 number
  // 做 `!==` 比较时恒为真（曾导致"配音版本与页面版本不匹配"而隐藏播放器）。
  return { slides: data.slides ?? [], revisionNo: Number(data.revisionNo) || 0 };
}

// SourceRevisionSummary 是版本历史列表项（原生 HTTP GET /projects/{pid}/revisions 返回）。
export type SourceRevisionSummary = {
  revisionNo: number;
  createdAt: string; // RFC3339
  pageCount: number;
  parserVersion: string;
  objectKey: string;
  displayName: string; // 默认取 objectKey 的文件名，可被前端覆盖
  isCurrent: boolean;
};

// parseObjectKeyDisplayName 从对象存储 key 提取文件名作为默认展示名。
function parseObjectKeyDisplayName(objectKey: string): string {
  const lastSlash = objectKey.lastIndexOf('/');
  return lastSlash >= 0 ? objectKey.slice(lastSlash + 1) : objectKey;
}

// getSourceRevisions 返回项目源版本历史（倒序）与当前生效版本号。
// 用于编辑器版本抽屉查看历史版本；切换"设为当前"不在此接口范围（后端暂无写接口）。
export async function getSourceRevisions(
  identity: ClientIdentity,
  projectId: string
): Promise<{ currentRevision: number; revisions: SourceRevisionSummary[] }> {
  const r = await getJSON<{
    current_revision: number;
    revisions: Array<{
      revision_no: number;
      created_at: string;
      page_count: number;
      parser_version: string;
      object_key: string;
      display_name?: string;
      is_current: boolean;
    }>;
  }>(identity, `/projects/${encodeURIComponent(projectId)}/revisions`);
  return {
    currentRevision: r.current_revision,
    revisions: (r.revisions ?? []).map((x) => ({
      revisionNo: x.revision_no,
      createdAt: x.created_at,
      pageCount: x.page_count,
      parserVersion: x.parser_version,
      objectKey: x.object_key,
      displayName: x.display_name || parseObjectKeyDisplayName(x.object_key),
      isCurrent: x.is_current,
    })),
  };
}

export async function deleteSourceRevision(identity: ClientIdentity, projectId: string, revisionNo: number): Promise<void> {
  await deleteJSON<{ ok: boolean }>(identity, `/projects/${encodeURIComponent(projectId)}/revisions/${revisionNo}`);
}

export async function updatePptDisplayName(identity: ClientIdentity, projectId: string, revisionNo: number, displayName: string): Promise<void> {
  const response = await fetch(`/projects/${encodeURIComponent(projectId)}/revisions/${revisionNo}`, {
    method: 'PATCH',
    headers: { ...identityHeaders(identity), 'Content-Type': 'application/json' },
    body: JSON.stringify({ display_name: displayName })
  });
  if (!response.ok) {
    let message = `/projects/${encodeURIComponent(projectId)}/revisions/${revisionNo} failed: HTTP ${response.status}`;
    try {
      const envelope = (await response.json()) as { code?: string; message?: string };
      if (envelope.code) message = `${envelope.code}: ${envelope.message ?? ''}`;
    } catch { /* ignore */ }
    throw new ConnectError('http_' + response.status, message);
  }
}

export async function downloadSourceRevision(
  identity: ClientIdentity,
  projectId: string,
  revisionNo: number,
  filename: string
): Promise<void> {
  const response = await fetch(`/projects/${encodeURIComponent(projectId)}/revisions/${revisionNo}/download`, {
    headers: identityHeaders(identity)
  });
  if (!response.ok) {
    throw new Error(`download source revision failed: HTTP ${response.status}`);
  }
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename || `presentation-v${revisionNo}.pptx`;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

// getSlideNotes 读取单页备注（空字符串 = 无备注）。
export async function getSlideNotes(identity: ClientIdentity, projectId: string, slideId: string, revisionNo: number): Promise<string> {
  const r = await getJSON<{ notes: string }>(identity, `/projects/${encodeURIComponent(projectId)}/slides/${encodeURIComponent(slideId)}/notes?revision_no=${revisionNo}`);
  return r.notes ?? '';
}

// setSlideNotes 保存单页备注。
export async function setSlideNotes(identity: ClientIdentity, projectId: string, slideId: string, revisionNo: number, notes: string): Promise<void> {
  const response = await fetch(`/projects/${encodeURIComponent(projectId)}/slides/${encodeURIComponent(slideId)}/notes?revision_no=${revisionNo}`, {
    method: 'PATCH',
    headers: { ...identityHeaders(identity), 'Content-Type': 'application/json' },
    body: JSON.stringify({ notes })
  });
  if (!response.ok) {
    let message = `setSlideNotes failed: HTTP ${response.status}`;
    try {
      const envelope = (await response.json()) as { code?: string; message?: string };
      if (envelope.code) message = `${envelope.code}: ${envelope.message ?? ''}`;
    } catch { /* ignore */ }
    throw new ConnectError('http_' + response.status, message);
  }
}

export type SlideRenderURL = { slideId: string; url: string };

// getSlideRenderURLs 返回每页渲染 PNG 的短期签名可读 URL（按 slideId 对齐），供编辑器缩略图与 PPT 预览使用。
// 解析未完成或页面图缺失时返回空列表，前端优雅降级为序号/标题缩略图。
export async function getSlideRenderURLs(
  identity: ClientIdentity,
  projectId: string,
  revisionNo?: number
): Promise<{ slides: SlideRenderURL[] }> {
  const qs = revisionNo && revisionNo > 0 ? `?revision_no=${revisionNo}` : '';
  return getJSON<{ slides: SlideRenderURL[] }>(identity, `/projects/${encodeURIComponent(projectId)}/slides/render${qs}`);
}

export type SlideScriptSource = { slideId: string; source: string; customText: string };

// setSlideScriptSource 持久化单页讲稿来源选择（M3 ⑥，无备注页显式指定驱动草稿来源）。
export async function setSlideScriptSource(
  identity: ClientIdentity,
  projectId: string,
  slideId: string,
  source: string,
  customText = ''
): Promise<SlideScriptSource> {
  return putJSON<SlideScriptSource>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/slides/${encodeURIComponent(slideId)}/source`,
    { source, customText }
  );
}

// getSlideScriptSources 读取项目内所有页的讲稿来源选择（M3 ⑥）。
export async function getSlideScriptSources(
  identity: ClientIdentity,
  projectId: string
): Promise<{ sources: SlideScriptSource[] }> {
  return getJSON<{ sources: SlideScriptSource[] }>(identity, `/projects/${encodeURIComponent(projectId)}/slides/sources`);
}

export async function getScript(
  identity: ClientIdentity,
  projectId: string,
  slideId: string
): Promise<ScriptRevision> {
  return connectJSON<ScriptRevision>(identity, '/ppts.v1.ScriptService/Get', { projectId, slideId });
}

export async function listProjectScripts(
  identity: ClientIdentity,
  projectId: string
): Promise<{ scripts: ScriptRevision[] }> {
  return getJSON<{ scripts: ScriptRevision[] }>(identity, `/projects/${encodeURIComponent(projectId)}/scripts`);
}

export type UpdateScriptResult = {
  revision: ScriptRevision;
  conflict: boolean;
  latest?: ScriptRevision;
};

export async function updateScript(
  identity: ClientIdentity,
  projectId: string,
  slideId: string,
  expectedRevision: number,
  segments: ScriptSegment[]
): Promise<UpdateScriptResult> {
  return connectJSON<UpdateScriptResult>(identity, '/ppts.v1.ScriptService/Update', {
    projectId,
    slideId,
    expectedRevision,
    segments
  });
}

// 讲稿「确认 / 锁定」的客户端封装（approveScript / lockScript / unlockScript）已移除：
// 产品决定改为「按需编辑、不锁定」（commit 40387c6 的免确认编辑），后端
// ScriptService/Approve、ScriptService/Lock 能力保留，但前端不再有入口，故不留死导出。

// regenerateSegments 局部重生成选中分段（M2 M1 落地的 RegenerateSegments RPC）。
// 返回 jobId；生成完成后需重新拉取讲稿。后端 NarrationService.RegenerateSegments。
export async function regenerateSegments(
  identity: ClientIdentity,
  projectId: string,
  slideId: string,
  segmentIds: string[],
  voiceId?: string
): Promise<{ jobId: string }> {
  const idempotencyKey = `regen-${projectId}-${slideId}-${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return connectJSON<{ jobId: string }>(identity, '/ppts.v1.NarrationService/RegenerateSegments', {
    projectId,
    slideId,
    segmentIds,
    voiceId: voiceId ?? ''
  }, { 'Idempotency-Key': idempotencyKey });
}

export async function rewriteScriptText(
  identity: ClientIdentity,
  projectId: string,
  slideId: string,
  text: string,
  action: 'shorten' | 'polish' | 'transition' | 'ai_generated'
): Promise<{ text: string }> {
  return postJSON<{ text: string }>(identity, `/projects/${encodeURIComponent(projectId)}/scripts/rewrite`, {
    slideId,
    text,
    action
  });
}

// 说明（M6 清理）：此处原有 generateDraft（Connect ScriptService/GenerateDraft）已删除——
// 全站无调用方，且它未传 RevisionNo/SourceMode，一旦被启用会让 worker 回退到首个版本
// （旧版本可能页数不足/没有备注 → "有备注的页没有讲稿"）。重生成讲稿统一走
// regenerateScriptDraft（原生端点，显式绑定当前版本并透传 sourceMode/overwrite）。

// regenerateScriptDraft 重新生成讲稿。默认 overwrite=true；一键成稿可传 overwrite=false 只填充空白页。
export async function regenerateScriptDraft(
  identity: ClientIdentity,
  projectId: string,
  slideIds: string[],
  mode: ScriptMode,
  options: {
    language?: string;
    sourceMode?: 'notes_first' | 'page_content' | 'notes_only';
    audience?: string;
    style?: string;
    targetSeconds?: number;
    overwrite?: boolean;
  } = {}
): Promise<{ jobId: string }> {
  return postJSON<{ jobId: string }>(identity, `/projects/${encodeURIComponent(projectId)}/script-draft`, {
    slideIds,
    mode,
    language: options.language,
    sourceMode: options.sourceMode,
    audience: options.audience,
    style: options.style,
    targetSeconds: options.targetSeconds,
    overwrite: options.overwrite
  });
}

export async function createGeneration(
  identity: ClientIdentity,
  projectId: string,
  slideIds: string[],
  voiceId: string,
  idempotencyKey: string,
  opts: { ratePercent?: number; lockConfirmedOnly?: boolean } = {}
): Promise<{ jobId: string; withinBudget: boolean }> {
  // D0-1：前端此前漏传 ratePercent / lockConfirmedOnly，导致后端 C-5 强制阻止未确认稿与
  // 配额预占比例从未生效。这里补全：ratePercent 默认 100（全速），lockConfirmedOnly 默认 false。
  return connectJSON<{ jobId: string; withinBudget: boolean }>(
    identity,
    '/ppts.v1.NarrationService/CreateGeneration',
    {
      projectId,
      slideIds,
      voiceId,
      ratePercent: opts.ratePercent ?? 100,
      lockConfirmedOnly: opts.lockConfirmedOnly ?? false
    },
    { 'Idempotency-Key': idempotencyKey }
  );
}

export type NarrationEstimate = {
  currency: string;
  costMin: number;
  costMax: number;
  estimatedSeconds: number;
};

export async function estimateNarration(
  identity: ClientIdentity,
  projectId: string,
  slideIds: string[],
  voiceId: string,
  ratePercent = 100
): Promise<NarrationEstimate> {
  const data = await connectJSON<{ currency?: string; costMin?: number; costMax?: number; estimatedSeconds?: number }>(
    identity,
    '/ppts.v1.NarrationService/Estimate',
    { projectId, slideIds, voiceId, ratePercent }
  );
  return {
    currency: data.currency ?? '',
    costMin: data.costMin ?? 0,
    costMax: data.costMax ?? 0,
    estimatedSeconds: data.estimatedSeconds ?? 0
  };
}

// getNarrationDraftCount 读取项目内仍处于 draft 状态的讲稿分段总数（M1 端点，C-5 生成前置检查用）。
// 返回 { draftSegments }，>0 表示存在未确认讲稿，正式生成应被阻止。
export async function getNarrationDraftCount(
  identity: ClientIdentity,
  projectId: string
): Promise<{ draftSegments: number }> {
  return getJSON<{ draftSegments: number }>(identity, `/projects/${encodeURIComponent(projectId)}/narration/draft-count`);
}

export type ProjectArtifact = {
  id: string;
  snapshotHash: string;
  format: 'mp4' | 'srt' | 'vtt' | 'web_project';
  sizeBytes: number;
  // durationMs：成品所绑定时间轴的实际时长；0 = 未知/未记录（迁移 0027 之前的历史行），显示「—」。
  durationMs: number;
  createdAt: string;
  downloadable: boolean;
};

// getProjectArtifacts 读取项目下全部产物（B3-M1 原生端点，前端按 snapshotHash 分组展示与下载）。
export async function getProjectArtifacts(
  identity: ClientIdentity,
  projectId: string
): Promise<{ artifacts: ProjectArtifact[] }> {
  return getJSON<{ artifacts: ProjectArtifact[] }>(identity, `/projects/${encodeURIComponent(projectId)}/artifacts`);
}

// getLibraryArtifacts 读取租户内全部项目的成品（B5-M2 跨项目成品库，owner 级）。
// 返回项含 projectId / projectName，前端按格式 / 项目 / 时间筛选，并可跳转项目成品页。
export type LibraryArtifact = {
  id: string;
  projectId: string;
  projectName: string;
  snapshotHash: string;
  format: 'mp4' | 'srt' | 'vtt' | 'web_project';
  sizeBytes: number;
  // durationMs：0 = 未知/未记录（迁移 0027 之前的历史行），显示「—」。
  durationMs: number;
  createdAt: string;
  downloadable: boolean;
  // previewable：成品绑定了导出时的时间轴（迁移 0040 之后导出），可内嵌预览；
  // false（历史行）时前端隐藏"预览"按钮，降级为仅下载。
  previewable: boolean;
  // revisionNo：成品所源自的源版本号（0 = 未知/历史行），与 sourceDisplayName 一并展示"PPT 名称 vN"；
  // 由 narration 写入时间轴、export 落库（见 internal/app/{narration,export}.go）。
  revisionNo?: number;
  // sourceDisplayName：源版本展示名（source_revisions.display_name，空 = 未知），
  // 空时前端回退到 projectName。
  sourceDisplayName?: string;
};

export async function getLibraryArtifacts(identity: ClientIdentity): Promise<{ artifacts: LibraryArtifact[] }> {
  return getJSON<{ artifacts: LibraryArtifact[] }>(identity, '/artifacts');
}

// getArtifactManifest 取成品内嵌预览用的播放清单（GET /artifacts/{id}/manifest）：
// 以成品绑定的时间轴为数据源，与下载文件同源；返回结构与 Player 的 PlaybackManifest 一致。
export async function getArtifactManifest(identity: ClientIdentity, artifactId: string): Promise<PlaybackManifest> {
  return getJSON<PlaybackManifest>(identity, `/artifacts/${encodeURIComponent(artifactId)}/manifest`);
}

// deleteArtifact 删除成品库中的成品（DELETE /artifacts/{id}，owner 级，与 GET /artifacts 同门禁）。
export async function deleteArtifact(identity: ClientIdentity, artifactId: string): Promise<{ deleted: boolean; id: string }> {
  return deleteJSON<{ deleted: boolean; id: string }>(identity, `/artifacts/${encodeURIComponent(artifactId)}`);
}

export type NarrationSlideStale = { slideId: string; stale: boolean };

export type RevisionVoiceStatus = {
  revisionNo: number;
  status: 'not_voiced' | 'partial' | 'complete' | 'stale';
  pageCount: number;
  voicedPages: number;
  missingPages: number;
  stalePages: number;
  missingSlideIds?: string[];
  staleSlideIds?: string[];
};

export async function getRevisionVoiceStatus(
  identity: ClientIdentity,
  projectId: string
): Promise<{ revisions: RevisionVoiceStatus[] }> {
  return getJSON<{ revisions: RevisionVoiceStatus[] }>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/revisions/voice-status`
  );
}

// getNarrationStale 读取项目内"讲稿已改、配音未重生成"的页（首页「音频需更新」的真实信号）。
// 后端判定：audio_revision < revision（internal/api/narration.go:373），要求 editor 角色。
export async function getNarrationStale(
  identity: ClientIdentity,
  projectId: string
): Promise<{ slides: NarrationSlideStale[] }> {
  return getJSON<{ slides: NarrationSlideStale[] }>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/narration/stale`
  );
}

export type NarrationStatus = {
  ready: boolean;
  timelineKey: string;
  pagePngKeys: string[];
  revisionNo: number;
};

export async function getNarration(identity: ClientIdentity, projectId: string): Promise<NarrationStatus> {
  const data = await connectJSON<NarrationStatus & { revisionNo?: number | string }>(
    identity,
    '/ppts.v1.PlaybackService/GetNarration',
    { projectId }
  );
  // protojson 的 int64 → 字符串，归一为 number（见 getProjectSlides 注释）。
  return { ...data, revisionNo: Number(data.revisionNo) || 0 };
}

// getRevisionNarration 读取指定源版本的配音状态（ready/timelineKey/pagePngKeys/revisionNo）。
// 后端按 snapshot.RevisionNo 归集该版本自己的成功配音任务（见 editorRevisionNarration），
// 供 PPT 列表页「导出」按钮就地弹 ExportDialog 时取素材。
export async function getRevisionNarration(
  identity: ClientIdentity,
  projectId: string,
  revisionNo: number
): Promise<NarrationStatus> {
  const data = await getJSON<NarrationStatus & { revisionNo?: number | string }>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/revisions/${revisionNo}/narration`
  );
  return { ...data, revisionNo: Number(data.revisionNo) || 0 };
}

export type UploadSession = {
  uploadId: string;
  signedUploadUrls: string[];
  chunkSizeBytes: number;
  objectKey: string;
};

export type CompletedUpload = {
  sourceRevisionId: string;
  jobId: string;
  warnings?: string[];
};

export async function createUpload(
  identity: ClientIdentity,
  projectId: string,
  filename: string,
  sizeBytes: number
): Promise<UploadSession> {
  return connectJSON<UploadSession>(identity, '/ppts.v1.UploadService/CreateUpload', {
    projectId,
    filename,
    sizeBytes
  });
}

export async function uploadToURL(
  url: string,
  file: File,
  opts: { onProgress?: (loaded: number, total: number) => void; signal?: AbortSignal } = {}
): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('PUT', url, true);
    xhr.setRequestHeader('Content-Type', file.type || 'application/octet-stream');
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable && opts.onProgress) opts.onProgress(event.loaded, event.total);
    };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) resolve();
      else reject(new Error(`upload failed: HTTP ${xhr.status}`));
    };
    xhr.onerror = () => reject(new Error('upload failed: network error'));
    xhr.onabort = () => reject(new DOMException('upload canceled', 'AbortError'));
    if (opts.signal) {
      if (opts.signal.aborted) {
        xhr.abort();
        return;
      }
      opts.signal.addEventListener('abort', () => xhr.abort(), { once: true });
    }
    xhr.send(file);
  });
}

export async function completeUpload(
  identity: ClientIdentity,
  uploadId: string,
  expectedHash: string,
  sizeBytes: number
): Promise<CompletedUpload> {
  return connectJSON<CompletedUpload>(
    identity,
    '/ppts.v1.UploadService/CompleteUpload',
    { uploadId, expectedHash, sizeBytes }
  );
}

export async function abortUpload(identity: ClientIdentity, uploadId: string): Promise<void> {
  await connectJSON<Record<string, never>>(identity, '/ppts.v1.UploadService/AbortUpload', { uploadId });
}

export async function listAuditEvents(
  identity: ClientIdentity,
  params: { action?: string; resourceType?: string; sinceUnix?: number; pageSize?: number }
): Promise<AuditEvent[]> {
  const data = await connectJSON<{ events?: AuditEvent[] }>(identity, '/ppts.v1.TenantService/ListAuditEvents', {
    action: params.action ?? '',
    resourceType: params.resourceType ?? '',
    sinceUnix: params.sinceUnix ?? 0,
    pageSize: params.pageSize ?? 50
  });
  return data.events ?? [];
}

export async function listAuditArchives(identity: ClientIdentity, limit = 20): Promise<AuditArchiveFile[]> {
  const data = await connectJSON<{ files?: AuditArchiveFile[] }>(identity, '/ppts.v1.TenantService/ListAuditArchives', { limit });
  return data.files ?? [];
}

export async function sha256Hex(file: File): Promise<string> {
  const buffer = await file.arrayBuffer();
  const digest = await crypto.subtle.digest('SHA-256', buffer);
  return Array.from(new Uint8Array(digest))
    .map((byte) => byte.toString(16).padStart(2, '0'))
    .join('');
}

// ---- 模型网关管理（G3 可视化配置，admin only）----

export type ModelGateway = {
  tenantId: string;
  name: string;
  kind: 'tts' | 'llm';
  provider: string;
  baseUrl: string;
  model: string;
  visionModel: string;
  voice: string;
  sampleRate: number;
  isDefault: boolean;
  enabled: boolean;
  version: number;
  hasKey: boolean;
  keyMasked: string;
  createdAt: number;
  updatedAt: number;
};

export type GatewayTestResult = { ok: boolean; latencyMs: number; error?: string };

function gatewayPath(identity: ClientIdentity, method: string, path: string, body?: unknown): Promise<unknown> {
  const url = path;
  const response = fetch(url, {
    method,
    headers: { 'Content-Type': 'application/json', ...identityHeaders(identity) },
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  return response.then(async (r) => {
    if (!r.ok) {
      let message = `${method} ${url} failed: HTTP ${r.status}`;
      try {
        const envelope = (await r.json()) as { message?: string };
        if (envelope.message) message = envelope.message;
      } catch {
        // 保留默认 message。
      }
      throw new ConnectError(`http_${r.status}`, message);
    }
    if (r.status === 204) return undefined;
    return r.json();
  });
}

export async function listGateways(identity: ClientIdentity, kind?: 'tts' | 'llm'): Promise<ModelGateway[]> {
  const suffix = kind ? `?kind=${kind}` : '';
  const data = (await gatewayPath(identity, 'GET', `/api/model-gateways${suffix}`)) as { gateways?: ModelGateway[] };
  return data.gateways ?? [];
}

export async function createGateway(
  identity: ClientIdentity,
  input: { kind: 'tts' | 'llm'; name: string; baseUrl: string; apiKey: string; model: string; provider?: string; visionModel?: string; voice?: string; sampleRate?: number; isDefault?: boolean }
): Promise<ModelGateway> {
  const data = (await gatewayPath(identity, 'POST', '/api/model-gateways', input)) as { gateway?: ModelGateway };
  return data.gateway!;
}

export async function updateGateway(
  identity: ClientIdentity,
  name: string,
  input: { kind: 'tts' | 'llm'; originalKind?: 'tts' | 'llm'; version: number; baseUrl?: string; apiKey?: string; model?: string; provider?: string; visionModel?: string; voice?: string; sampleRate?: number; isDefault?: boolean; enabled?: boolean }
): Promise<ModelGateway> {
  const data = (await gatewayPath(identity, 'PUT', `/api/model-gateways/${encodeURIComponent(name)}`, input)) as { gateway?: ModelGateway };
  return data.gateway!;
}

export async function deleteGateway(identity: ClientIdentity, name: string, kind: 'tts' | 'llm'): Promise<void> {
  await gatewayPath(identity, 'DELETE', `/api/model-gateways/${encodeURIComponent(name)}?kind=${kind}`);
}

export async function setDefaultGateway(identity: ClientIdentity, name: string, kind: 'tts' | 'llm'): Promise<void> {
  await gatewayPath(identity, 'POST', `/api/model-gateways/${encodeURIComponent(name)}/set-default?kind=${kind}`);
}

export async function testGateway(identity: ClientIdentity, name: string, kind: 'tts' | 'llm'): Promise<GatewayTestResult> {
  return (await gatewayPath(identity, 'POST', `/api/model-gateways/${encodeURIComponent(name)}/test?kind=${kind}`)) as GatewayTestResult;
}

// ---- 项目语音属性（语音模型 / 音色 / 语速）----

export type ProjectVoiceSettings = {
  model: string;
  voice: string;
  ratePercent: number;
};

export type VoiceModel = {
  name: string;
  model: string;
  voices: string[];
  isDefault: boolean;
};

export async function getVoiceSettings(identity: ClientIdentity, projectId: string): Promise<ProjectVoiceSettings> {
  return getJSON<ProjectVoiceSettings>(identity, `/projects/${encodeURIComponent(projectId)}/voice-settings`);
}

export async function saveVoiceSettings(
  identity: ClientIdentity,
  projectId: string,
  settings: ProjectVoiceSettings
): Promise<ProjectVoiceSettings> {
  return putJSON<ProjectVoiceSettings>(identity, `/projects/${encodeURIComponent(projectId)}/voice-settings`, {
    model: settings.model,
    voice: settings.voice,
    ratePercent: settings.ratePercent
  });
}

// listVoiceModels 返回本租户已启用的 TTS 模型及其配置的音色；无可用模型时返回空数组。
export async function listVoiceModels(identity: ClientIdentity, projectId: string): Promise<VoiceModel[]> {
  const data = await getJSON<{ models?: VoiceModel[] }>(identity, `/projects/${encodeURIComponent(projectId)}/voice-models`);
  return data.models ?? [];
}

// ---- 任务（JobService） ----

export async function listJobs(identity: ClientIdentity, projectId?: string): Promise<Job[]> {
  const data = await connectJSON<{ jobs?: Job[] }>(identity, '/ppts.v1.JobService/List', {
    projectId: projectId ?? '',
    pageSize: 50
  });
  return data.jobs ?? [];
}

export type JobPage = { jobs: Job[]; nextCursor: string };

export async function listJobsPage(
  identity: ClientIdentity,
  params: { projectId?: string; cursor?: string; pageSize: number }
): Promise<JobPage> {
  const data = await connectJSON<{ jobs?: Job[]; nextCursor?: { value?: string } }>(
    identity,
    '/ppts.v1.JobService/List',
    {
      projectId: params.projectId ?? '',
      pageSize: params.pageSize,
      ...(params.cursor ? { cursor: { value: params.cursor } } : {})
    }
  );
  return { jobs: data.jobs ?? [], nextCursor: data.nextCursor?.value ?? '' };
}

export async function getJob(identity: ClientIdentity, jobId: string): Promise<Job> {
  return connectJSON<Job>(identity, '/ppts.v1.JobService/Get', { jobId });
}

// ---- 任务扩展详情（B4-M6a：原生 HTTP 端点；本环境 protoc 不可用，故不改 proto）----
//
// 后端 internal/api/jobdetail.go：
//   GET /jobs/{jid}/detail  —— traceId + 范围（由 input_snapshot 推导）+ 执行步骤
//   GET /jobs/summary?ids=… —— 批量「范围 / 阶段 / 步骤计数」，供任务列表两列（避免逐任务查询）
// 权限与 JobService.Get/List 同级（viewer 可见，租户隔离），无需额外能力判定。

export type JobStepState = 'pending' | 'success' | 'skipped' | 'failed';

// JobStep 是一步执行记录；resultRef 属内部对象键，后端只回 hasResult。
export type JobStep = {
  stepType: string;
  state: JobStepState;
  updatedAtUnix: number;
  hasResult: boolean;
};

// JobScope 是任务范围摘要：kind 为范围性质，pageCount 为受影响页数；
// pageCount > affectedPages.length 表示页面数组被后端截断（计数仍完整）。
export type JobScope = {
  kind: 'project' | 'pages' | 'segments' | 'export' | 'unknown';
  pageCount: number;
  affectedPages: string[];
  inputRevision: number;
  format?: string;
};

// JobExtras 是列表侧的单任务扩展信息（summary 端点）。
// B4-M6b 收窄：只回范围。阶段改由 /jobs/page 的 jobs[].phase 提供（唯一来源 jobs.phase），
// 步骤计数只在任务详情里展示——原先的 phase/stepTotal/stepCounts 前端从未消费，已一并去掉。
export type JobExtras = {
  scope: JobScope;
};

export type JobDetail = {
  jobId: string;
  kind: string;
  traceId: string;
  scope: JobScope;
  steps: JobStep[];
  stepCounts: Record<string, number>;
  stepTotal: number;
  stepsTruncated: boolean;
  // stepsError：'unsupported'（后端 store 未提供步骤读取能力）| 'load_failed'（读取失败）。
  // 非空时 steps 必为空数组，界面必须显示原因，不得显示成"没有步骤"。
  stepsError?: string;
};

export async function getJobDetail(identity: ClientIdentity, jobId: string): Promise<JobDetail> {
  return getJSON<JobDetail>(identity, `/jobs/${encodeURIComponent(jobId)}/detail`);
}

export async function getJobsSummary(
  identity: ClientIdentity,
  jobIds: string[]
): Promise<{ jobs: Record<string, JobExtras> }> {
  return getJSON<{ jobs: Record<string, JobExtras> }>(
    identity,
    `/jobs/summary?ids=${encodeURIComponent(jobIds.join(','))}`
  );
}

// ---- B4-M6b：任务列表的筛选 / 排序 / 翻页（原生 HTTP 端点）----
// 为什么不复用 listJobsPage（proto JobService.List）：本环境 protoc 不可用，无法为它加
// sort/phase 参数；此端点还改用 (排序键, id) 的 keyset 游标，翻页不受并发插入影响。

export type JobListSort = 'created' | 'updated' | 'phase' | 'pages';

// JobListRow 是列表行：Job 的基本字段 + 阶段（来自 jobs.phase，空串=尚无步骤）。
export type JobListRow = Job & { phase: string };

export type JobsPageResult = {
  jobs: JobListRow[];
  nextCursor: string;
  // phaseCounts：各阶段的任务数，用于筛选下拉展示可选值与数量；读取失败时给出 phaseCountsError。
  phaseCounts?: Record<string, number>;
  phaseCountsError?: string;
};

export async function getJobsPage(
  identity: ClientIdentity,
  params: { phase?: string; sort: JobListSort; desc: boolean; pageSize: number; cursor?: string }
): Promise<JobsPageResult> {
  const query = new URLSearchParams();
  if (params.phase) query.set('phase', params.phase);
  query.set('sort', params.sort);
  query.set('dir', params.desc ? 'desc' : 'asc');
  query.set('limit', String(params.pageSize));
  if (params.cursor) query.set('cursor', params.cursor);
  const data = await getJSON<{
    jobs?: JobListRow[];
    nextCursor?: string;
    phaseCounts?: Record<string, number>;
    phaseCountsError?: string;
  }>(identity, `/jobs/page?${query.toString()}`);
  return {
    jobs: data.jobs ?? [],
    nextCursor: data.nextCursor ?? '',
    phaseCounts: data.phaseCounts,
    phaseCountsError: data.phaseCountsError
  };
}

export async function cancelJob(identity: ClientIdentity, jobId: string): Promise<Job> {
  return connectJSON<Job>(identity, '/ppts.v1.JobService/Cancel', { jobId });
}

export async function retryFailedJob(identity: ClientIdentity, jobId: string): Promise<Job> {
  return connectJSON<Job>(identity, '/ppts.v1.JobService/RetryFailed', { jobId });
}

export type JobEventMessage = { seq: number; job: Job };

// watchJobEvents 接入 WatchEvents 服务端流（Connect 协议 JSON 信封）。
// 解析「1 字节 flag + 4 字节大端长度 + JSON 消息」的信封流，逐条回调 onEvent。
// 任一错误（HTTP 非 2xx、信封解析失败、网络中断）均回调 onError，由调用方决定回退轮询；
// 调用方应传入 AbortSignal 以便在组件卸载时取消。
export function watchJobEvents(
  identity: ClientIdentity,
  projectId: string,
  afterSeq: number,
  handlers: { onEvent: (ev: JobEventMessage) => void; onError?: (err: unknown) => void },
  signal?: AbortSignal
): void {
  const headers: Record<string, string> = {
    'Content-Type': 'application/connect+json',
    Accept: 'application/connect+json',
    ...identityHeaders(identity)
  };
  void fetch('/ppts.v1.JobService/WatchEvents', {
    method: 'POST',
    headers,
    body: JSON.stringify({ projectId, afterSeq }),
    signal
  })
    .then(async (response) => {
      if (!response.ok || !response.body) {
        handlers.onError?.(new ConnectError(`http_${response.status}`, `WatchEvents failed: HTTP ${response.status}`));
        return;
      }
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = new Uint8Array(0);
      const append = (chunk: Uint8Array) => {
        const next = new Uint8Array(buffer.length + chunk.length);
        next.set(buffer);
        next.set(chunk, buffer.length);
        buffer = next;
      };
      const readFrame = (): JobEventMessage | null => {
        if (buffer.length < 5) return null;
        const flag = buffer[0];
        if (flag !== 0x00) {
          // 仅支持未压缩信封（connect-go 对短消息不压缩）；压缩/未知 → 交给调用方回退。
          throw new Error('unexpected envelope flag');
        }
        const len = ((buffer[1] << 24) | (buffer[2] << 16) | (buffer[3] << 8) | buffer[4]) >>> 0;
        if (len < 0 || buffer.length < 5 + len) return null;
        const data = buffer.slice(5, 5 + len);
        buffer = buffer.slice(5 + len);
        const text = decoder.decode(data);
        return JSON.parse(text) as JobEventMessage;
      };
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        if (value) append(value);
        try {
          for (;;) {
            const frame = readFrame();
            if (!frame) break;
            handlers.onEvent(frame);
          }
        } catch (e) {
          handlers.onError?.(e);
          return;
        }
      }
    })
    .catch((err) => {
      if ((err as Error).name === 'AbortError') return;
      handlers.onError?.(err);
    });
}

// ---- 租户（TenantService） ----

export async function listMembers(identity: ClientIdentity): Promise<Member[]> {
  const data = await getJSON<{ members?: {
    user_id: string;
    role: Role;
    created_at?: string;
    email?: string;
    username?: string;
    full_name?: string;
    gender?: string;
    birth_date?: string;
    phone?: string;
  }[] }>(identity, '/members');
  return (data.members ?? []).map((member) => ({
    userId: member.user_id,
    role: member.role,
    createdAt: member.created_at,
    email: member.email,
    username: member.username,
    fullName: member.full_name,
    gender: member.gender,
    birthDate: member.birth_date,
    phone: member.phone
  }));
}

// updateMemberProfile 写入成员档案（admin 级），供成员列表富字段展示。
export async function updateMemberProfile(
  identity: ClientIdentity,
  userId: string,
  profile: Partial<Pick<Member, 'username' | 'fullName' | 'gender' | 'birthDate' | 'phone'>>
): Promise<void> {
  await putJSON(identity, `/members/${encodeURIComponent(userId)}`, profile as Record<string, unknown>);
}

// ---- #94 标签 + 分组体系（原生 HTTP 端点） ----

export async function listTags(identity: ClientIdentity): Promise<Tag[]> {
  const data = await getJSON<{ tags?: Tag[] }>(identity, '/tags');
  return data.tags ?? [];
}

export async function createTag(identity: ClientIdentity, name: string, color?: string): Promise<Tag> {
  return postJSON<Tag>(identity, '/tags', { name, color: color ?? '' });
}

export async function renameTag(identity: ClientIdentity, tagId: string, name: string, color?: string): Promise<Tag> {
  return putJSON<Tag>(identity, `/tags/${encodeURIComponent(tagId)}`, { name, color: color ?? '' });
}

export async function deleteTag(identity: ClientIdentity, tagId: string): Promise<void> {
  await deleteJSON<{ ok: boolean }>(identity, `/tags/${encodeURIComponent(tagId)}`);
}

export async function listFolders(identity: ClientIdentity): Promise<Folder[]> {
  const data = await getJSON<{ folders?: {
    id: string;
    name: string;
    created_by?: string;
    sort_order?: number;
    created_at?: string;
  }[] }>(identity, '/folders');
  return (data.folders ?? []).map((folder) => ({
    id: folder.id,
    name: folder.name,
    createdBy: folder.created_by,
    sortOrder: folder.sort_order,
    createdAt: folder.created_at
  }));
}

export async function createFolder(identity: ClientIdentity, name: string, afterFolderId = ''): Promise<Folder> {
  const folder = await postJSON<{ id: string; name: string; created_by?: string; sort_order?: number; created_at?: string }>(identity, '/folders', {
    name,
    after_folder_id: afterFolderId
  });
  return {
    id: folder.id,
    name: folder.name,
    createdBy: folder.created_by,
    sortOrder: folder.sort_order,
    createdAt: folder.created_at
  };
}

export async function renameFolder(identity: ClientIdentity, folderId: string, name: string): Promise<Folder> {
  return putJSON<Folder>(identity, `/folders/${encodeURIComponent(folderId)}`, { name });
}

export async function deleteFolder(identity: ClientIdentity, folderId: string): Promise<void> {
  await deleteJSON<{ ok: boolean }>(identity, `/folders/${encodeURIComponent(folderId)}`);
}

export async function listProjectOrganization(identity: ClientIdentity): Promise<ProjectOrg[]> {
  const data = await getJSON<{ organization?: ProjectOrg[] }>(identity, '/projects/organization');
  return data.organization ?? [];
}

export async function attachTag(identity: ClientIdentity, projectId: string, tagId: string): Promise<void> {
  await putJSON<{ ok: boolean }>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/tags/${encodeURIComponent(tagId)}`,
    {}
  );
}

export async function detachTag(identity: ClientIdentity, projectId: string, tagId: string): Promise<void> {
  await deleteJSON<{ ok: boolean }>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/tags/${encodeURIComponent(tagId)}`
  );
}

export async function moveProject(identity: ClientIdentity, projectId: string, folderId: string): Promise<void> {
  await putJSON<{ ok: boolean }>(identity, `/projects/${encodeURIComponent(projectId)}/folder`, { folderId });
}

export async function setMemberRole(identity: ClientIdentity, userId: string, role: Role): Promise<Member> {
  const data = await connectJSON<{ member?: Member }>(identity, '/ppts.v1.TenantService/SetMemberRole', { userId, role });
  return data.member!;
}

export async function removeMember(identity: ClientIdentity, userId: string): Promise<void> {
  await connectJSON<Record<string, never>>(identity, '/ppts.v1.TenantService/RemoveMember', { userId });
}

export async function getQuota(identity: ClientIdentity): Promise<TenantQuota> {
  return connectJSON<TenantQuota>(identity, '/ppts.v1.TenantService/Quota', {});
}

export async function getUsage(identity: ClientIdentity, month?: string): Promise<TenantUsage> {
  return connectJSON<TenantUsage>(identity, '/ppts.v1.TenantService/Usage', { month: month ?? '' });
}

export async function getProjectUsage(identity: ClientIdentity, projectId: string): Promise<ProjectUsage> {
  return connectJSON<ProjectUsage>(identity, '/ppts.v1.TenantService/ProjectUsage', { projectId });
}

export async function getStorageUsage(identity: ClientIdentity): Promise<StorageUsage> {
  return connectJSON<StorageUsage>(identity, '/ppts.v1.TenantService/StorageUsage', {});
}

export async function getPolicy(identity: ClientIdentity): Promise<TenantPolicy> {
  return connectJSON<TenantPolicy>(identity, '/ppts.v1.TenantService/Policy', {});
}

// ---- 导出（ExportService） ----

export async function createExport(
  identity: ClientIdentity,
  params: {
    projectId: string;
    format: ArtifactFormat;
    timelineKey: string;
    pagePngKeys: string[];
    burnSubtitles?: boolean;
    includeNotes?: boolean;
    idempotencyKey: string;
  }
): Promise<{ jobId: string }> {
  return connectJSON<{ jobId: string }>(
    identity,
    '/ppts.v1.ExportService/CreateExport',
    {
      projectId: params.projectId,
      format: params.format,
      timelineKey: params.timelineKey,
      pagePngKeys: params.pagePngKeys,
      burnSubtitles: params.burnSubtitles,
      includeNotes: params.includeNotes
    },
    { 'Idempotency-Key': params.idempotencyKey }
  );
}

export async function createDownload(identity: ClientIdentity, artifactId: string, ttlSeconds = 900): Promise<{ signedUrl: string; expiresAtUnix: number }> {
  return connectJSON<{ signedUrl: string; expiresAtUnix: number }>(identity, '/ppts.v1.ExportService/CreateDownload', {
    artifactId,
    ttlSeconds
  });
}

// ---- 发音词典（/api/pronunciation，需认证，tenant 隔离） ----

export async function listDictionaries(identity: ClientIdentity): Promise<PronunciationDictionary[]> {
  const data = (await gatewayPath(identity, 'GET', '/api/pronunciation')) as { dictionaries?: PronunciationDictionary[] };
  return data.dictionaries ?? [];
}

export async function createDictionary(identity: ClientIdentity, input: { name: string; rules: PronunciationRule[] }): Promise<PronunciationDictionary> {
  const data = (await gatewayPath(identity, 'POST', '/api/pronunciation', input)) as { dictionary?: PronunciationDictionary };
  return data.dictionary!;
}

export async function updateDictionary(identity: ClientIdentity, id: string, input: { name: string; rules: PronunciationRule[] }): Promise<PronunciationDictionary> {
  const data = (await gatewayPath(identity, 'PUT', `/api/pronunciation/${encodeURIComponent(id)}`, input)) as { dictionary?: PronunciationDictionary };
  return data.dictionary!;
}

export async function deleteDictionary(identity: ClientIdentity, id: string): Promise<void> {
  await gatewayPath(identity, 'DELETE', `/api/pronunciation/${encodeURIComponent(id)}`);
}

// ---- 公开区（/public/*，V1.6 C-1） ----
// 匿名只读接口（list/get）无需身份；写接口（发布/精选/审核/删除）带身份头。

export type PublicationKind = 'featured' | 'user';
export type PublicationStatus = 'draft' | 'pending' | 'approved' | 'rejected' | 'withdrawn';

// PublicWork 字段名与后端 JSON（snake_case）一致，避免额外映射层。
export type PublicWork = {
  id: string;
  public_id?: string;
  tenant_id: string;
  project_id: string;
  kind: PublicationKind;
  status: PublicationStatus;
  title: string;
  summary: string;
  cover_object_key?: string;
  cover_url?: string;
  sort_order: number;
  created_by: string;
  created_at: string; // RFC3339
  updated_at?: string;
  reviewed_by?: string;
  reviewed_at?: string;
};

export type PublicWorkPage = { items: PublicWork[]; next_cursor: string };

async function publicGet<T>(path: string): Promise<T> {
  const response = await fetch(path);
  if (!response.ok) {
    // 解析后端 {code,message} 错误体（如公开区在单租户部署返回 feature_disabled/503），
    // 使 describeApiError 能映射为可读文案；无 JSON 体时回退 HTTP 码。
    let code = `http_${response.status}`;
    let message = `GET ${path} failed: HTTP ${response.status}`;
    try {
      const envelope = (await response.json()) as { code?: string; message?: string };
      if (envelope.code) code = envelope.code;
      if (envelope.message) message = envelope.message;
    } catch {
      // 非 JSON 错误体，保留默认 message。
    }
    throw new ConnectError(code, message);
  }
  return (await response.json()) as T;
}

export async function listPublicWorks(params: { kind?: PublicationKind; cursor?: string; limit?: number } = {}): Promise<PublicWorkPage> {
  const qs = new URLSearchParams();
  if (params.kind) qs.set('kind', params.kind);
  if (params.cursor) qs.set('cursor', params.cursor);
  if (params.limit) qs.set('limit', String(params.limit));
  const q = qs.toString();
  return publicGet<PublicWorkPage>(`/public/works${q ? `?${q}` : ''}`);
}

// getShowcaseWork 拉取已批准公开作品详情（B5-M3：按不可反推的 public_id 匿名访问 /showcase/{publicId}）。
export async function getShowcaseWork(publicId: string): Promise<PublicWork> {
  return publicGet<PublicWork>(`/showcase/${encodeURIComponent(publicId)}`);
}

// getShowcaseManifest 拉取已批准公开作品的匿名可播放讲解清单（B3 音频播放接入）。
// 与控制台 getPlaybackManifest 同构，但走原生 HTTP 匿名端点、无需鉴权；narration 未就绪时返回 404。
export async function getShowcaseManifest(publicId: string): Promise<PlaybackManifest> {
  return publicGet<PlaybackManifest>(`/showcase/${encodeURIComponent(publicId)}/manifest`);
}

export async function publishWork(
  identity: ClientIdentity,
  input: { projectId: string; title: string; summary?: string; coverObjectKey?: string }
): Promise<PublicWork> {
  return connectJSON<PublicWork>(identity, '/public/works', {
    project_id: input.projectId,
    title: input.title,
    summary: input.summary ?? '',
    cover_object_key: input.coverObjectKey ?? ''
  });
}

export async function featureWork(
  identity: ClientIdentity,
  input: { projectId: string; title: string; summary?: string; coverObjectKey?: string }
): Promise<PublicWork> {
  return connectJSON<PublicWork>(identity, '/public/featured', {
    project_id: input.projectId,
    title: input.title,
    summary: input.summary ?? '',
    cover_object_key: input.coverObjectKey ?? ''
  });
}

// authedJSON 兼容非 POST 方法（审核用 PUT、删除用 DELETE）。
async function authedJSON<T>(identity: ClientIdentity, method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json', ...identityHeaders(identity) },
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  if (!response.ok) {
    let code = `http_${response.status}`;
    let message = `${method} ${path} failed: HTTP ${response.status}`;
    try {
      const envelope = (await response.json()) as { code?: string; message?: string };
      if (envelope.code) code = envelope.code;
      if (envelope.message) message = envelope.message;
    } catch {
      // 保留默认 message。
    }
    throw new ConnectError(code, message);
  }
  if (response.status === 204) return undefined as T;
  return (await response.json()) as T;
}

export async function reviewWork(identity: ClientIdentity, id: string, approve: boolean): Promise<PublicWork> {
  return authedJSON<PublicWork>(identity, 'PUT', `/public/works/${encodeURIComponent(id)}/review`, { approve });
}

export async function deleteWork(identity: ClientIdentity, id: string): Promise<void> {
  await authedJSON<Record<string, never>>(identity, 'DELETE', `/public/works/${encodeURIComponent(id)}`);
}

// recallWork 由 owner/admin 撤回已发布作品（置 withdrawn 立即失效，B5-M3）。
export async function recallWork(identity: ClientIdentity, publicId: string): Promise<PublicWork> {
  return authedJSON<PublicWork>(identity, 'POST', `/public/works/${encodeURIComponent(publicId)}/recall`);
}

// 受保护只读：我的发布（按创建者）/ 审核队列（admin，pending）。
export async function listMyPublications(identity: ClientIdentity, params: { status?: PublicationStatus } = {}): Promise<PublicWorkPage> {
  const qs = new URLSearchParams();
  if (params.status) qs.set('status', params.status);
  const q = qs.toString();
  return (await authedJSON<PublicWorkPage>(identity, 'GET', `/public/works/mine${q ? `?${q}` : ''}`)) as PublicWorkPage;
}

export async function listReviewQueue(identity: ClientIdentity, params: { kind?: PublicationKind } = {}): Promise<PublicWorkPage> {
  const qs = new URLSearchParams();
  if (params.kind) qs.set('kind', params.kind);
  const q = qs.toString();
  return (await authedJSON<PublicWorkPage>(identity, 'GET', `/public/works/queue${q ? `?${q}` : ''}`)) as PublicWorkPage;
}

// ---- 私密分享与协作者（#95） ----
// 与"发布到公开作品广场"是两种不同能力：私密分享不出现在广场，只面向持有链接的人。
// 管理端走原生 HTTP 受保护端点；匿名端走 /shared/{token} 最小字段端点。

export async function listCollaborators(identity: ClientIdentity, projectId: string): Promise<Collaborator[]> {
  const data = await getJSON<{ collaborators?: Collaborator[] }>(identity, `/projects/${encodeURIComponent(projectId)}/collaborators`);
  return data.collaborators ?? [];
}

export async function inviteCollaborator(
  identity: ClientIdentity,
  projectId: string,
  params: { email: string; role: string }
): Promise<Collaborator> {
  const data = await postJSON<{ collaborator?: Collaborator }>(identity, `/projects/${encodeURIComponent(projectId)}/collaborators`, params);
  return data.collaborator!;
}

export async function updateCollaboratorRole(
  identity: ClientIdentity,
  projectId: string,
  userId: string,
  role: string
): Promise<Collaborator> {
  const data = await putJSON<{ collaborator?: Collaborator }>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/collaborators/${encodeURIComponent(userId)}`,
    { role }
  );
  return data.collaborator!;
}

export async function removeCollaborator(identity: ClientIdentity, projectId: string, userId: string): Promise<void> {
  await deleteJSON<{ ok?: boolean }>(identity, `/projects/${encodeURIComponent(projectId)}/collaborators/${encodeURIComponent(userId)}`);
}

export async function listShareLinks(identity: ClientIdentity, projectId: string): Promise<ShareLink[]> {
  const data = await getJSON<{ shareLinks?: ShareLink[] }>(identity, `/projects/${encodeURIComponent(projectId)}/shares`);
  return data.shareLinks ?? [];
}

export async function createShareLink(
  identity: ClientIdentity,
  projectId: string,
  params: { accessMode: string; password?: string; expiresInDays?: number }
): Promise<ShareLink> {
  const data = await postJSON<{ shareLink?: ShareLink }>(identity, `/projects/${encodeURIComponent(projectId)}/shares`, params);
  return data.shareLink!;
}

export async function revokeShareLink(identity: ClientIdentity, projectId: string, linkId: string): Promise<void> {
  await postJSON<{ ok?: boolean }>(identity, `/projects/${encodeURIComponent(projectId)}/shares/${encodeURIComponent(linkId)}/revoke`, {});
}

// 匿名端：口令只经请求头传递（不进 URL / 访问日志）。
async function sharedGet<T>(path: string, password?: string): Promise<T> {
  const headers: Record<string, string> = {};
  if (password) headers['X-Share-Password'] = password;
  const response = await fetch(path, { headers });
  if (!response.ok) {
    throw new Error(`GET ${path} failed: HTTP ${response.status}`);
  }
  return (await response.json()) as T;
}

export function getSharedMeta(token: string): Promise<SharedMeta> {
  return sharedGet<SharedMeta>(`/shared/${encodeURIComponent(token)}`);
}

export function getSharedManifest(token: string, password?: string): Promise<PlaybackManifest> {
  return sharedGet<PlaybackManifest>(`/shared/${encodeURIComponent(token)}/manifest`, password);
}

// shareUrl 把后端给的相对路径拼成绝对地址，供"复制链接"使用。
export function shareUrl(url: string): string {
  if (!url) return '';
  if (/^https?:\/\//i.test(url)) return url;
  return `${window.location.origin}${url.startsWith('/') ? url : `/${url}`}`;
}
