import { useRef, useState } from 'react';
import type { PointerEvent as ReactPointerEvent } from 'react';

// 拖拽上下限：讲稿栏不窄于 280，预览区不被压塌。
const minPanelWidth = 280;
const minPreviewWidth = 300;
const railWidth = 200;
const layoutGaps = 30;
// 除讲稿栏外必须留出的宽度（左页轨 + 预览最小宽 + 三处间距）。
const reservedWidth = railWidth + minPreviewWidth + layoutGaps;

/**
 * 预览区与讲稿栏之间竖条的拖拽。
 *
 * 宽度只写进 CSS 变量 --script-panel-width（当前会话生效，不落库）：
 * 布局状态留在 DOM 上，避免拖拽过程中每帧触发一次 React 重渲染。
 */
export function useScriptPanelResize() {
  const layoutRef = useRef<HTMLDivElement>(null);
  const [resizing, setResizing] = useState(false);

  const startResize = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const layout = layoutRef.current;
    if (!layout) return;
    event.preventDefault();
    const handle = event.currentTarget;
    const pointerId = event.pointerId;
    handle.setPointerCapture?.(pointerId);
    setResizing(true);
    const rect = layout.getBoundingClientRect();
    const onMove = (moveEvent: PointerEvent) => {
      const max = Math.max(minPanelWidth, rect.width - reservedWidth);
      const width = Math.min(Math.max(rect.right - moveEvent.clientX, minPanelWidth), max);
      layout.style.setProperty('--script-panel-width', `${Math.round(width)}px`);
    };
    const onEnd = () => {
      setResizing(false);
      handle.releasePointerCapture?.(pointerId);
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onEnd);
      window.removeEventListener('pointercancel', onEnd);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onEnd);
    window.addEventListener('pointercancel', onEnd);
  };

  return { layoutRef, resizing, startResize };
}
