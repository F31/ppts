import type { PlaybackManifest, Project, ScriptRevision, SlideSummary } from './types';

export type ClientIdentity = {
  tenantId: string;
  userId: string;
};

export class ConnectError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = 'ConnectError';
    this.code = code;
  }
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
      'X-PPTS-Tenant-ID': params.identity.tenantId,
      'X-PPTS-User-ID': params.identity.userId
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
      'X-PPTS-Tenant-ID': identity.tenantId,
      'X-PPTS-User-ID': identity.userId,
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

export async function generateDraft(
  identity: ClientIdentity,
  projectId: string,
  slideIds: string[]
): Promise<{ jobId: string; fully_supported: boolean }> {
  return connectJSON<{ jobId: string; fully_supported: boolean }>(
    identity,
    '/ppts.v1.ScriptService/GenerateDraft',
    { projectId, slideIds }
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

export async function sha256Hex(file: File): Promise<string> {
  const buffer = await file.arrayBuffer();
  const digest = await crypto.subtle.digest('SHA-256', buffer);
  return Array.from(new Uint8Array(digest))
    .map((byte) => byte.toString(16).padStart(2, '0'))
    .join('');
}
