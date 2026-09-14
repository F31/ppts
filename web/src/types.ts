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
