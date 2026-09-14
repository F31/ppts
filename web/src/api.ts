import type { AuditArchiveFile, AuditEvent, PlaybackManifest, Project, ScriptMode, ScriptRevision, ScriptSegment, SlideSummary } from './types';

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
  warnings: string[];
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

export async function uploadToURL(url: string, file: File): Promise<void> {
  const response = await fetch(url, {
    method: 'PUT',
    headers: { 'Content-Type': file.type || 'application/octet-stream' },
    body: file
  });
  if (!response.ok) {
    throw new Error(`upload failed: HTTP ${response.status}`);
  }
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
