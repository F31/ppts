import { useCallback, useEffect, useRef, useState } from 'react';
import { getFocusSuggestions, getFocusTargets, type ClientIdentity } from '../../api';
import { describeApiError } from '../../apiError';
import type { FocusSuggestion, FocusTarget } from '../../types';
import type { Translate } from './model';

type FocusTargets = { slideId: string; targets: FocusTarget[]; note: string };

type FocusSuggestions = {
  slideId: string;
  band: string;
  suggestions: FocusSuggestion[];
};

/**
 * 焦点候选与焦点建议的取数。
 *
 * 两者都锚在"当前页 + 源版本"上，并各自承担一条不可混淆的语义：
 *  - 候选读失败 ≠ 这一页没有可指的目标（必须显示"读失败"）；
 *  - 建议读失败 ≠ 已有焦点无效（建议只影响"有没有建议"）。
 *
 * 建议是**按需**拉取的：切页/换版本不会自动在后台算，更不会自动落库；
 * 采纳写的是讲稿段落 focus，走 ScriptEditor 既有的自动保存通道。
 */
export function useFocusAssist(
  identity: ClientIdentity,
  projectId: string,
  slideId: string,
  revisionNo: number,
  t: Translate
) {
  const [focusTargets, setFocusTargets] = useState<FocusTargets | null>(null);
  const [focusTargetsError, setFocusTargetsError] = useState<string | null>(null);

  const [focusSuggestions, setFocusSuggestions] = useState<FocusSuggestions | null>(null);
  const [focusSuggestionsStatus, setFocusSuggestionsStatus] = useState<'idle' | 'loading' | 'ready' | 'failed'>('idle');
  const [focusSuggestionsError, setFocusSuggestionsError] = useState<string | null>(null);
  // 请求序号：切页后上一页的在途响应必须作废，否则建议会串到新页讲稿上。
  const suggestionReqRef = useRef(0);

  useEffect(() => {
    if (!slideId) {
      setFocusTargets(null);
      setFocusTargetsError(null);
      return;
    }
    let cancelled = false;
    setFocusTargetsError(null);
    getFocusTargets(identity, projectId, slideId, revisionNo)
      .then((res) => {
        if (cancelled) return;
        setFocusTargets({ slideId: res.slideId, targets: res.targets ?? [], note: res.note ?? '' });
      })
      .catch((error) => {
        if (cancelled) return;
        setFocusTargets(null);
        // 读取失败只影响"能不能选"，不影响已有焦点与讲稿保存；但界面必须说"读失败"，
        // 不能显示成"这一页没有可指的目标"——那是两件事。
        setFocusTargetsError(describeApiError(error, t('editor.focusLoadFailed'), t));
      });
    return () => {
      cancelled = true;
    };
  }, [identity, projectId, slideId, revisionNo, t]);

  useEffect(() => {
    suggestionReqRef.current += 1;
    setFocusSuggestions(null);
    setFocusSuggestionsStatus('idle');
    setFocusSuggestionsError(null);
  }, [identity, projectId, slideId, revisionNo]);

  const loadFocusSuggestions = useCallback(() => {
    if (!slideId) return;
    const seq = ++suggestionReqRef.current;
    setFocusSuggestionsStatus('loading');
    setFocusSuggestionsError(null);
    // 与 focus-targets 用同一个源版本号：建议里的 shapeId 必须能在候选清单里找到。
    getFocusSuggestions(identity, projectId, slideId, revisionNo)
      .then((res) => {
        if (seq !== suggestionReqRef.current) return;
        setFocusSuggestions({
          slideId,
          band: res.band ?? '',
          suggestions: res.suggestions ?? []
        });
        setFocusSuggestionsStatus('ready');
      })
      .catch((error) => {
        if (seq !== suggestionReqRef.current) return;
        setFocusSuggestions(null);
        setFocusSuggestionsStatus('failed');
        // 读取失败只影响"有没有建议"，绝不能把已有焦点说成"没有可指的"。
        setFocusSuggestionsError(describeApiError(error, t('editor.focusSuggestLoadFailed'), t));
      });
  }, [identity, projectId, slideId, revisionNo, t]);

  return {
    focusTargets,
    focusTargetsError,
    focusSuggestions,
    focusSuggestionsStatus,
    focusSuggestionsError,
    loadFocusSuggestions
  };
}
