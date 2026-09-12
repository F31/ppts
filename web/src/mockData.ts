import type { PlaybackManifest, ScriptRevision, Timeline } from './types';

export const demoTimeline: Timeline = {
  schemaVersion: '1.0',
  durationUs: 8_900_000,
  slides: [
    {
      slideId: 'slide-01',
      startUs: 0,
      endUs: 3_200_000,
      segments: [{ segmentId: 'seg-01-01', audioKey: 'audio-1', startUs: 300_000, endUs: 2_700_000, alignmentMethod: 'estimated' }]
    },
    {
      slideId: 'slide-02',
      startUs: 3_200_000,
      endUs: 6_100_000,
      segments: [{ segmentId: 'seg-02-01', audioKey: 'audio-2', startUs: 3_500_000, endUs: 5_700_000, alignmentMethod: 'estimated' }]
    },
    {
      slideId: 'slide-03',
      startUs: 6_100_000,
      endUs: 8_900_000,
      segments: [{ segmentId: 'seg-03-01', audioKey: 'audio-3', startUs: 6_400_000, endUs: 8_500_000, alignmentMethod: 'estimated' }]
    }
  ],
  subtitles: [
    { slideId: 'slide-01', segmentId: 'seg-01-01', startUs: 300_000, endUs: 2_700_000, text: '本页介绍产品定位与目标用户。' },
    { slideId: 'slide-02', segmentId: 'seg-02-01', startUs: 3_500_000, endUs: 5_700_000, text: '核心流程包含上传、解析、讲稿确认和导出。' },
    { slideId: 'slide-03', segmentId: 'seg-03-01', startUs: 6_400_000, endUs: 8_500_000, text: '所有播放和导出都使用同一条时间轴。' }
  ]
};

export const demoManifest: PlaybackManifest = {
  projectId: 'demo-project',
  timelineKey: 'demo/timeline.json',
  timelineJson: JSON.stringify(demoTimeline),
  expiresAtUnix: Math.floor(Date.now() / 1000) + 3600,
  resources: [
    { type: 'PLAYBACK_RESOURCE_TYPE_PAGE_PNG', key: 'slide-01.png', signedUrl: '', contentType: 'image/png', sizeBytes: 0, contentHash: 'demo', slideId: 'slide-01' },
    { type: 'PLAYBACK_RESOURCE_TYPE_PAGE_PNG', key: 'slide-02.png', signedUrl: '', contentType: 'image/png', sizeBytes: 0, contentHash: 'demo', slideId: 'slide-02' },
    { type: 'PLAYBACK_RESOURCE_TYPE_PAGE_PNG', key: 'slide-03.png', signedUrl: '', contentType: 'image/png', sizeBytes: 0, contentHash: 'demo', slideId: 'slide-03' }
  ]
};

export const demoScripts: ScriptRevision[] = demoTimeline.slides.map((slide, index) => ({
  projectId: 'demo-project',
  slideId: slide.slideId,
  language: 'zh-CN',
  revision: index + 1,
  status: index === 2 ? 'draft' : 'approved',
  segments: [
    {
      segmentId: slide.segments[0].segmentId,
      slideId: slide.slideId,
      displayText: demoTimeline.subtitles[index].text,
      spokenText: demoTimeline.subtitles[index].text,
      status: index === 2 ? 'draft' : 'approved'
    }
  ]
}));
