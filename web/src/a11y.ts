import { useEffect, useRef } from 'react';

// V-M3：对话框可访问性 hook。
// 能力：打开时把焦点移入对话框（首个可聚焦元素或对话框本身）、Tab/Shift+Tab
// 在对话框内循环（焦点陷阱）、Esc 关闭、关闭后焦点返回触发元素。
// 用法：把返回的 ref 挂到 role="dialog" 元素上，并补 aria-modal="true"。
const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'textarea:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',');

export function useDialogA11y<T extends HTMLElement = HTMLDivElement>(
  onClose: () => void,
  active = true,
) {
  const ref = useRef<T>(null);
  // 用 ref 持有最新 onClose，避免父组件每次渲染传入新函数导致 effect 反复重挂载/重聚焦。
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    if (!active) return;
    const node = ref.current;
    if (!node) return;

    const trigger = document.activeElement as HTMLElement | null;
    if (!node.hasAttribute('tabindex')) node.tabIndex = -1;

    const getFocusable = () =>
      Array.from(node.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
        (el) => el.offsetWidth > 0 || el.offsetHeight > 0 || el === document.activeElement,
      );

    const focusable = getFocusable();
    (focusable[0] ?? node).focus();

    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        onCloseRef.current();
        return;
      }
      if (e.key !== 'Tab') return;
      const f = getFocusable();
      if (f.length === 0) {
        e.preventDefault();
        node.focus();
        return;
      }
      const first = f[0];
      const last = f[f.length - 1];
      const activeEl = document.activeElement as HTMLElement | null;
      if (e.shiftKey && activeEl === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && activeEl === last) {
        e.preventDefault();
        first.focus();
      }
    };

    node.addEventListener('keydown', onKeyDown);
    return () => {
      node.removeEventListener('keydown', onKeyDown);
      if (trigger && trigger.isConnected) trigger.focus();
    };
  }, [active]);

  return ref;
}
