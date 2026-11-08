import { useState } from 'react';
import { saveVoiceSettings, type ClientIdentity, type ProjectVoiceSettings } from '../../api';
import type { Translate } from './model';

export type VoiceDraft = {
  model: string;
  voice: string;
  ratePercent: number;
};

/**
 * 语音属性抽屉的草稿值与保存动作。
 *
 * 三件事必须一起管，否则会出现"界面显示 A、实际用 B"：
 *  ① 打开抽屉时用**已保存值**重置草稿（含把生成方式归位为增量）；
 *  ② 换模型后音色要跟着换成该模型的第一档——留着上一个模型的音色，
 *     保存时会被后端拒绝，或在成片里发出完全不同的声音；
 *  ③ 保存失败必须留在抽屉里并保留草稿值，不能关窗（否则用户不知道没存上）。
 *
 * 配音流水线不在这里：保存成功后由调用方带着返回值发起，避免本 hook 反向依赖任务编排。
 */
export function useVoiceDraft(args: {
  identity: ClientIdentity;
  projectId: string;
  t: Translate;
  /** 取某个模型可用音色；由调用方从模型清单里查，保持本 hook 不持有列表。 */
  voiceOptions: (model: string) => string[];
}) {
  const { identity, projectId, t, voiceOptions } = args;
  const [draft, setDraft] = useState<VoiceDraft>({ model: '', voice: '', ratePercent: 100 });
  const [genMode, setGenMode] = useState<'incremental' | 'full'>('incremental');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  /** 打开抽屉时重置：草稿回到已保存值，生成方式回到增量，清掉上一次的失败提示。 */
  const reset = (saved: VoiceDraft) => {
    setDraft({ model: saved.model, voice: saved.voice, ratePercent: saved.ratePercent });
    setGenMode('incremental');
    setError('');
  };

  /** 换模型：音色必须同步换成该模型的第一档，否则保存的是"别的模型的音色"。 */
  const selectModel = (name: string) => {
    setDraft((current) => ({ ...current, model: name, voice: voiceOptions(name)[0] ?? '' }));
  };

  const setVoice = (voice: string) => setDraft((current) => ({ ...current, voice }));
  const setRate = (ratePercent: number) => setDraft((current) => ({ ...current, ratePercent }));

  /** 保存语音配置；失败返回 null（错误已落到 error，抽屉保持打开）。 */
  const save = async (): Promise<ProjectVoiceSettings | null> => {
    if (!draft.voice) {
      setError(t('editor.voiceModelEmpty'));
      return null;
    }
    setSaving(true);
    setError('');
    try {
      return await saveVoiceSettings(identity, projectId, {
        model: draft.model,
        voice: draft.voice,
        ratePercent: draft.ratePercent
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : t('editor.voiceSaveFailed'));
      return null;
    } finally {
      setSaving(false);
    }
  };

  return {
    draft,
    genMode,
    saving,
    error,
    reset,
    selectModel,
    setVoice,
    setRate,
    setGenMode,
    save
  };
}
