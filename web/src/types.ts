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
  status: 'draft' | 'approved' | 'locked';
};

export type ScriptRevision = {
  projectId: string;
  slideId: string;
  language: string;
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
