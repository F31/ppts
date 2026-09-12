import type { PlaybackManifest, Project } from './types';

export type ClientIdentity = {
  tenantId: string;
  userId: string;
};

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

async function connectJSON<T>(identity: ClientIdentity, procedure: string, body: unknown): Promise<T> {
  const response = await fetch(procedure, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-PPTS-Tenant-ID': identity.tenantId,
      'X-PPTS-User-ID': identity.userId
    },
    body: JSON.stringify(body)
  });
  if (!response.ok) {
    throw new Error(`${procedure} failed: HTTP ${response.status}`);
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
