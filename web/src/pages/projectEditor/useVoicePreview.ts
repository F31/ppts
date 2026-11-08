import { useEffect, useRef, useState } from 'react';
import { ConnectError, previewVoice, type ClientIdentity } from '../../api';
import { describeApiError } from '../../apiError';
import { VoicePreviewSequencer, classifyVoicePreviewError, voicePreviewKey } from '../../voicePreview';
import type { Translate } from './model';

/**
 * 音色试听：只返回一次性 Blob，不落对象存储。
 *
 * 两条生命周期必须由持有者负责，否则会出两类"看起来像灵异"的问题：
 *  ① **串音**：换音色/语速或关掉弹窗后仍留着上一段样音，用户会以为"刚选的音色就是这个声音"。
 *     因此草稿值一变就 cancel 在途请求并清空结果；晚到的响应按 (音色, 语速) 键校验后丢弃。
 *  ② **泄漏**：Blob URL 不 revoke，每试听一次就留下一个不可回收的音频对象。
 *     因此替换与卸载都要释放。
 */
export function useVoicePreview(args: {
  identity: ClientIdentity;
  projectId: string;
  t: Translate;
  /** 草稿值（不是已保存值）：试听要听"尚未保存"的那一档。 */
  model: string;
  voice: string;
  ratePercent: number;
  /** 语音属性弹窗是否打开：关闭即视为放弃本次试听。 */
  panelOpen: boolean;
}) {
  const { identity, projectId, t, model, voice, ratePercent, panelOpen } = args;
  const [loading, setLoading] = useState(false);
  const [url, setURL] = useState('');
  const [error, setError] = useState('');
  const [resultModel, setResultModel] = useState('');
  const [durationMs, setDurationMs] = useState(0);
  const seq = useRef(new VoicePreviewSequencer());

  // 换音色/语速、或关掉弹窗后立即丢弃上一次样音。
  useEffect(() => {
    seq.current.cancel();
    setURL('');
    setError('');
    setResultModel('');
    setDurationMs(0);
    setLoading(false);
  }, [model, voice, ratePercent, panelOpen]);

  // Blob URL 必须在替换或卸载时释放。
  useEffect(() => {
    if (!url) return undefined;
    return () => URL.revokeObjectURL(url);
  }, [url]);

  // play：用**草稿**音色/语速合成一段样音，用户不必先保存再听。
  // 结果按 (音色, 语速) 键校验后才落地：上一次请求晚到不会覆盖当前选择。
  const play = async () => {
    if (!voice) {
      setError(t('editor.voiceModelEmpty'));
      return;
    }
    const request = { voice, ratePercent, model };
    const key = voicePreviewKey(request);
    const token = seq.current.begin(key);
    setLoading(true);
    setError('');
    try {
      const result = await previewVoice(identity, projectId, request);
      if (!seq.current.isCurrent(token, key)) return;
      setURL(URL.createObjectURL(result.blob));
      setResultModel(result.model);
      setDurationMs(result.durationMs);
    } catch (err) {
      if (!seq.current.isCurrent(token, key)) return;
      if (err instanceof ConnectError) {
        // 四种失败的处置不同：去配模型 / 稍后重试 / 改短文本 / 其它。
        switch (classifyVoicePreviewError(err.code)) {
          case 'not_configured':
            setError(t('editor.voicePreviewNoGateway'));
            break;
          case 'model_missing':
            // 与"没配任何模型"分开：模型清单可能只是那一行被停用了，
            // 提示用户重选即可，不必去查网关是否可用。
            setError(t('editor.voicePreviewModelMissing', { model }));
            break;
          case 'timeout':
            setError(t('editor.voicePreviewTimeout'));
            break;
          case 'too_long':
            setError(t('editor.voicePreviewTooLong'));
            break;
          default:
            setError(describeApiError(err, t('editor.voicePreviewFailed'), t));
        }
      } else {
        setError(describeApiError(err, t('editor.voicePreviewFailed'), t));
      }
    } finally {
      if (seq.current.isCurrent(token, key)) setLoading(false);
    }
  };

  return { loading, url, error, model: resultModel, durationMs, play };
}
