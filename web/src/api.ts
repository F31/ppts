import type {
  ArtifactFormat,
  AuditArchiveFile,
  AuditEvent,
  Job,
  Member,
  PlaybackManifest,
  Project,
  PronunciationDictionary,
  PronunciationRule,
  ProjectUsage,
  Role,
  ScriptMode,
  ScriptRevision,
  ScriptSegment,
  SlideSummary,
  StorageUsage,
  TenantPolicy,
  TenantQuota,
  TenantUsage
} from './types';

export type ClientIdentity = {
  tenantId: string;
  userId: string;
  accessToken?: string;
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

export async function listProjects(identity: ClientIdentity): Promise<Project[]> {
  const data = await connectJSON<{ projects?: Project[] }>(identity, '/ppts.v1.ProjectService/List', { pageSize: 20 });
  return data.projects ?? [];
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
  projectId: string
): Promise<{ revisionNo: number; slides: SlideSummary[] }> {
  return connectJSON<{ revisionNo: number; slides: SlideSummary[] }>(
    identity,
    '/ppts.v1.ProjectService/GetSlides',
    { projectId }
  );
}

export async function getScript(
  identity: ClientIdentity,
  projectId: string,
  slideId: string
): Promise<ScriptRevision> {
  return connectJSON<ScriptRevision>(identity, '/ppts.v1.ScriptService/Get', { projectId, slideId });
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

export async function generateDraft(
  identity: ClientIdentity,
  projectId: string,
  slideIds: string[],
  mode: ScriptMode = 'SCRIPT_MODE_ORIGINAL',
  options: { audience?: string; style?: string; totalSeconds?: number } = {}
): Promise<{ jobId: string; fullySupported: boolean }> {
  return connectJSON<{ jobId: string; fullySupported: boolean }>(
    identity,
    '/ppts.v1.ScriptService/GenerateDraft',
    {
      projectId,
      slideIds,
      mode,
      audience: options.audience,
      style: options.style,
      duration: options.totalSeconds ? { totalSeconds: options.totalSeconds } : undefined
    }
  );
}

export async function createGeneration(
  identity: ClientIdentity,
  projectId: string,
  slideIds: string[],
  voiceId: string,
  idempotencyKey: string
): Promise<{ jobId: string; withinBudget: boolean }> {
  return connectJSON<{ jobId: string; withinBudget: boolean }>(
    identity,
    '/ppts.v1.NarrationService/CreateGeneration',
    { projectId, slideIds, voiceId },
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
  voiceId: string
): Promise<NarrationEstimate> {
  const data = await connectJSON<{ currency?: string; costMin?: number; costMax?: number; estimatedSeconds?: number }>(
    identity,
    '/ppts.v1.NarrationService/Estimate',
    { projectId, slideIds, voiceId }
  );
  return {
    currency: data.currency ?? '',
    costMin: data.costMin ?? 0,
    costMax: data.costMax ?? 0,
    estimatedSeconds: data.estimatedSeconds ?? 0
  };
}

export type NarrationStatus = {
  ready: boolean;
  timelineKey: string;
  pagePngKeys: string[];
  revisionNo: number;
};

export async function getNarration(identity: ClientIdentity, projectId: string): Promise<NarrationStatus> {
  return connectJSON<NarrationStatus>(identity, '/ppts.v1.PlaybackService/GetNarration', { projectId });
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
  input: { kind: 'tts' | 'llm'; name: string; baseUrl: string; apiKey: string; model: string; visionModel?: string; voice?: string; sampleRate?: number; isDefault?: boolean }
): Promise<ModelGateway> {
  const data = (await gatewayPath(identity, 'POST', '/api/model-gateways', input)) as { gateway?: ModelGateway };
  return data.gateway!;
}

export async function updateGateway(
  identity: ClientIdentity,
  name: string,
  input: { kind: 'tts' | 'llm'; version: number; baseUrl?: string; apiKey?: string; model?: string; visionModel?: string; voice?: string; sampleRate?: number; isDefault?: boolean; enabled?: boolean }
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

export async function cancelJob(identity: ClientIdentity, jobId: string): Promise<Job> {
  return connectJSON<Job>(identity, '/ppts.v1.JobService/Cancel', { jobId });
}

export async function retryFailedJob(identity: ClientIdentity, jobId: string): Promise<Job> {
  return connectJSON<Job>(identity, '/ppts.v1.JobService/RetryFailed', { jobId });
}

// ---- 租户（TenantService） ----

export async function listMembers(identity: ClientIdentity): Promise<Member[]> {
  const data = await connectJSON<{ members?: Member[] }>(identity, '/ppts.v1.TenantService/Members', {});
  return data.members ?? [];
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
  params: { projectId: string; format: ArtifactFormat; timelineKey: string; pagePngKeys: string[]; burnSubtitles?: boolean; includeNotes?: boolean }
): Promise<{ jobId: string }> {
  return connectJSON<{ jobId: string }>(identity, '/ppts.v1.ExportService/CreateExport', {
    projectId: params.projectId,
    format: params.format,
    timelineKey: params.timelineKey,
    pagePngKeys: params.pagePngKeys,
    burnSubtitles: params.burnSubtitles,
    includeNotes: params.includeNotes
  });
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
export type PublicationStatus = 'draft' | 'pending' | 'approved' | 'rejected';

// PublicWork 字段名与后端 JSON（snake_case）一致，避免额外映射层。
export type PublicWork = {
  id: string;
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
    throw new Error(`GET ${path} failed: HTTP ${response.status}`);
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

export async function getPublicWork(id: string): Promise<PublicWork> {
  return publicGet<PublicWork>(`/public/works/${encodeURIComponent(id)}`);
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


