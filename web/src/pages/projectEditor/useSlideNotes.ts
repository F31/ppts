import { useCallback, useRef, useState } from 'react';
import { getSlideNotes, setSlideNotes, type ClientIdentity } from '../../api';
import type { Translate } from './model';

/** 备注自动保存的防抖窗口：与失焦保存配合，避免每次按键都打服务端。 */
const notesSaveDebounceMs = 600;

/**
 * 当前页备注的读取与延迟保存。
 *
 * 备注不是讲稿：它随幻灯片切换/修订号变化重新加载，编辑后在失焦时延迟提交。
 * 这里只管"取与存"，不评判内容——写入讲稿是另一条显式动作（见页内 copyNotesToScript）。
 */
export function useSlideNotes(identity: ClientIdentity, projectId: string, t: Translate) {
  const [text, setText] = useState('');
  const [slideID, setSlideID] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const saveTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const load = useCallback(
    async (sid: string, revNo: number) => {
      setError('');
      try {
        const notes = await getSlideNotes(identity, projectId, sid, revNo);
        setText(notes ?? '');
        setSlideID(sid);
      } catch (err) {
        setError(err instanceof Error ? err.message : '');
      }
    },
    [identity, projectId]
  );

  const scheduleSave = useCallback(
    (next: string, sid: string, revNo: number) => {
      if (saveTimer.current) clearTimeout(saveTimer.current);
      setSaving(true);
      saveTimer.current = setTimeout(async () => {
        try {
          await setSlideNotes(identity, projectId, sid, revNo, next);
          setError('');
        } catch (err) {
          setError(err instanceof Error ? err.message : t('editor.notesSaveFailed'));
        } finally {
          setSaving(false);
        }
      }, notesSaveDebounceMs);
    },
    [identity, projectId, t]
  );

  return { text, slideID, saving, error, setText, load, scheduleSave };
}
