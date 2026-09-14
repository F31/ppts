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
  lastError?: { code: string; message: string };
  inputSnapshot: string;
  createdAtUnix: number;
  updatedAtUnix: number;
};

export const jobStateLabel: Record<JobState, string> = {
  JOB_STATE_QUEUED: '排队中',
  JOB_STATE_RUNNING: '处理中',
  JOB_STATE_RETRY_WAIT: '等待重试',
  JOB_STATE_WAITING_REVIEW: '待审阅',
  JOB_STATE_SUCCEEDED: '已完成',
  JOB_STATE_FAILED: '失败',
  JOB_STATE_CANCEL_REQUESTED: '正在取消',
  JOB_STATE_CANCELED: '已取消',
  JOB_STATE_UNKNOWN_PROVIDER_RESULT: '结果待核实'
};

export const jobKindLabel: Record<string, string> = {
  parse: '解析',
  render: '页面渲染',
  script_draft: '讲稿生成',
  narration: '配音生成',
  export: '导出'
};

// ---- 租户（TenantService） ----

export type Role = 'ROLE_OWNER' | 'ROLE_ADMIN' | 'ROLE_EDITOR' | 'ROLE_REVIEWER' | 'ROLE_VIEWER';

export const roleLabel: Record<Role, string> = {
  ROLE_OWNER: '所有者',
  ROLE_ADMIN: '管理员',
  ROLE_EDITOR: '编辑',
  ROLE_REVIEWER: '审阅',
  ROLE_VIEWER: '只读'
};

export type Member = { userId: string; role: Role };

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