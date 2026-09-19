export type AlignmentMethod = 'provider_timestamps' | 'forced_alignment' | 'estimated';

export type SegmentCue = {
  segmentId: string;
  audioKey: string;
  startUs: number;
  endUs: number;
  alignmentMethod?: AlignmentMethod;
};

export type SlideCue = {
  slideId: string;
  startUs: number;
  endUs: number;
  segments: SegmentCue[];
};

export type SubtitleCue = {
  slideId: string;
  segmentId: string;
  startUs: number;
  endUs: number;
  text: string;
};

export type Timeline = {
  schemaVersion: string;
  durationUs: number;
  slides: SlideCue[];
  subtitles: SubtitleCue[];
};

export type PlaybackResourceType =
  | 'PLAYBACK_RESOURCE_TYPE_TIMELINE'
  | 'PLAYBACK_RESOURCE_TYPE_PAGE_PNG'
  | 'PLAYBACK_RESOURCE_TYPE_AUDIO'
  | 'PLAYBACK_RESOURCE_TYPE_SUBTITLE_SRT'
  | 'PLAYBACK_RESOURCE_TYPE_SUBTITLE_VTT';

export type PlaybackResource = {
  type: PlaybackResourceType;
  key: string;
  signedUrl: string;
  contentType: string;
  sizeBytes: number;
  contentHash: string;
  slideId?: string;
  segmentId?: string;
};

export type PlaybackManifest = {
  projectId: string;
  timelineKey: string;
  timelineJson: string;
  resources: PlaybackResource[];
  expiresAtUnix: number;
};

export type ScriptSegment = {
  segmentId: string;
  slideId: string;
  displayText: string;
  spokenText: string;
  sourceRefs?: string[];
  sourceAnchors?: SourceAnchor[];
  status: 'draft' | 'approved' | 'locked';
};

export type ScriptMode = 'SCRIPT_MODE_ORIGINAL' | 'SCRIPT_MODE_POLISH' | 'SCRIPT_MODE_AI_GENERATED';

export type SourceAnchor = {
  slideId: string;
  shapeId: string;
  kind: string;
  raw: string;
  confidence: number;
};

export type ScriptRevision = {
  projectId: string;
  slideId: string;
  language: string;
  mode?: ScriptMode;
  revision: number;
  status: 'draft' | 'approved' | 'locked';
  segments: ScriptSegment[];
};

export type Project = {
  id: string;
  tenantId: string;
  owner: string;
  title: string;
  currentRevision: number;
  archived: boolean;
  createdAtUnix: number;
};

export type SlideSummary = {
  slideId: string;
  index: number;
  title: string;
  preview: string;
  hasNotes: boolean;
  featureFlags: string[];
};

export type AuditEvent = {
  id: string;
  actorUser: string;
  action: string;
  resourceType: string;
  resourceId: string;
  metadataJson: string;
  createdAtUnix: number;
};

export type AuditArchiveFile = {
  objectKey: string;
  sizeBytes: number;
  updatedAtUnix: number;
};

// ---- 任务（JobService） ----

export type JobState =
  | 'JOB_STATE_QUEUED'
  | 'JOB_STATE_RUNNING'
  | 'JOB_STATE_RETRY_WAIT'
  | 'JOB_STATE_WAITING_REVIEW'
  | 'JOB_STATE_SUCCEEDED'
  | 'JOB_STATE_FAILED'
  | 'JOB_STATE_CANCEL_REQUESTED'
  | 'JOB_STATE_CANCELED'
  | 'JOB_STATE_UNKNOWN_PROVIDER_RESULT';

export type Job = {
  jobId: string;
  projectId: string;
  kind: string;
  state: JobState;
  attempt: number;
  progressPercent: number;
  lastError?: { code: string; message: string; traceId?: string };
  inputSnapshot: string;
  createdAtUnix: number;
  updatedAtUnix: number;
};

export const jobStateKey: Record<JobState, string> = {
  JOB_STATE_QUEUED: 'enum.jobState.queued',
  JOB_STATE_RUNNING: 'enum.jobState.running',
  JOB_STATE_RETRY_WAIT: 'enum.jobState.retryWait',
  JOB_STATE_WAITING_REVIEW: 'enum.jobState.waitingReview',
  JOB_STATE_SUCCEEDED: 'enum.jobState.succeeded',
  JOB_STATE_FAILED: 'enum.jobState.failed',
  JOB_STATE_CANCEL_REQUESTED: 'enum.jobState.cancelRequested',
  JOB_STATE_CANCELED: 'enum.jobState.canceled',
  JOB_STATE_UNKNOWN_PROVIDER_RESULT: 'enum.jobState.unknownProviderResult'
};

export const jobKindKey: Record<string, string> = {
  parse: 'enum.jobKind.parse',
  render: 'enum.jobKind.render',
  script_draft: 'enum.jobKind.scriptDraft',
  narration: 'enum.jobKind.narration',
  export: 'enum.jobKind.export'
};

// B4-M6a：任务步骤类型/状态与范围种类（原生端点 /jobs/{id}/detail、/jobs/summary 的取值）。
// 步骤类型在库里是自由文本（migrations/0001_init.sql:78），此处只登记**当前 handler 实际写入**的取值
// （app/ingest.go:123 "pages"、app/narration.go:331 "tts_segment"、app/narration.go:475 "timeline"、
// app/export.go:64 "export"）；未登记的值界面直接显示原文，不做猜测性翻译。
export const jobStepTypeKey: Record<string, string> = {
  pages: 'enum.jobStepType.pages',
  tts_segment: 'enum.jobStepType.ttsSegment',
  timeline: 'enum.jobStepType.timeline',
  export: 'enum.jobStepType.export'
};

export const jobStepStateKey: Record<string, string> = {
  pending: 'enum.jobStepState.pending',
  success: 'enum.jobStepState.success',
  skipped: 'enum.jobStepState.skipped',
  failed: 'enum.jobStepState.failed'
};

export const jobScopeKindKey: Record<string, string> = {
  project: 'enum.jobScope.project',
  pages: 'enum.jobScope.pages',
  segments: 'enum.jobScope.segments',
  export: 'enum.jobScope.export',
  unknown: 'enum.jobScope.unknown'
};

// ---- 租户（TenantService） ----

export type Role = 'ROLE_OWNER' | 'ROLE_ADMIN' | 'ROLE_EDITOR' | 'ROLE_REVIEWER' | 'ROLE_VIEWER';

export const roleKey: Record<Role, string> = {
  ROLE_OWNER: 'enum.role.owner',
  ROLE_ADMIN: 'enum.role.admin',
  ROLE_EDITOR: 'enum.role.editor',
  ROLE_REVIEWER: 'enum.role.reviewer',
  ROLE_VIEWER: 'enum.role.viewer'
};

export type Member = {
  userId: string;
  role: Role;
  createdAt?: string; // 加入租户时间（RFC3339）
  email?: string; // 登录账号
  username?: string; // 展示用用户名（可空）
  fullName?: string; // 姓名
  gender?: string; // 性别 male/female/other/unknown
  birthDate?: string; // 出生年月 YYYY-MM-DD
  phone?: string; // 电话
};

export type TenantQuota = {
  monthlySeconds: number;
  usedSeconds: number;
  maxConcurrentJobs: number;
  maxStorageBytes: number;
};

export type TenantUsage = {
  secondsUsed: number;
  costUnits: number;
  userAmount: number;
  supplierCost: number;
  currency: string;
};

export type ProjectUsage = {
  projectId: string;
  seconds: number;
  jobCount: number;
  userAmount: number;
  supplierCost: number;
  currency: string;
};

export type StorageUsage = {
  sourceBytes: number;
  artifactBytes: number;
  totalBytes: number;
  sourceObjects: number;
  artifactObjects: number;
  otherBytes: number;
  otherObjects: number;
};

export type TenantPolicy = {
  storageBackend: string;
  storageRegion: string;
  sourceRetentionDays: number;
  envelopeEncryption: boolean;
  deleteSourceAfterDefault: boolean;
  storageTransitionDays: number;
  storageExpirationDays: number;
};

// ---- 导出（ExportService） ----

export type ArtifactFormat =
  | 'ARTIFACT_FORMAT_WEB_PROJECT'
  | 'ARTIFACT_FORMAT_MP4'
  | 'ARTIFACT_FORMAT_AUDIO_PACK'
  | 'ARTIFACT_FORMAT_SUBTITLE_SRT'
  | 'ARTIFACT_FORMAT_SUBTITLE_VTT'
  | 'ARTIFACT_FORMAT_AUDIO_PPTX';

export const artifactFormatLabel: Record<ArtifactFormat, string> = {
  ARTIFACT_FORMAT_WEB_PROJECT: 'Web 讲解工程',
  ARTIFACT_FORMAT_MP4: 'MP4 视频',
  ARTIFACT_FORMAT_AUDIO_PACK: '音频包',
  ARTIFACT_FORMAT_SUBTITLE_SRT: 'SRT 字幕',
  ARTIFACT_FORMAT_SUBTITLE_VTT: 'VTT 字幕',
  ARTIFACT_FORMAT_AUDIO_PPTX: '配音 PPTX'
};

export type Artifact = {
  artifactId: string;
  projectId: string;
  snapshotId: number;
  format: ArtifactFormat;
  objectKey: string;
  contentHash: string;
  sizeBytes: number;
  createdAtUnix: number;
  snapshotHash: string;
};

// ---- 发音词典 ----

export type PronunciationRule = { pattern: string; replacement: string; enabled: boolean };

export type PronunciationDictionary = {
  id: string;
  tenantId: string;
  name: string;
  rules: PronunciationRule[];
  updatedAtUnix?: number;
};