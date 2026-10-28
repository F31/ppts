import React from 'react';
import { Composition, registerRoot } from 'remotion';
import TimelineComposition, { totalFrames } from './Timeline';
import type { Timeline } from './types';

/**
 * 合成 ID 是 Go 侧 `renderer/render.mjs` 的约定值，改名要同步改那边。
 */
export const COMPOSITION_ID = 'Timeline';

/** defaultProps 只用于 Studio 里点开就有画面；真实渲染一律由 props 覆盖。 */
const emptyTimeline: Timeline = {
  schemaVersion: 'compiled-timeline/v1',
  fps: 30,
  width: 1920,
  height: 1080,
  background: '#FFFFFF',
  pages: []
};

const Root: React.FC = () => {
  return (
    <Composition
      id={COMPOSITION_ID}
      component={TimelineComposition}
      durationInFrames={30}
      fps={30}
      width={1920}
      height={1080}
      defaultProps={emptyTimeline}
      calculateMetadata={({ props }) => {
        const timeline = props as Timeline;
        return {
          durationInFrames: totalFrames(timeline),
          fps: timeline.fps || 30,
          width: timeline.width || 1920,
          height: timeline.height || 1080
        };
      }}
    />
  );
};

registerRoot(Root);
