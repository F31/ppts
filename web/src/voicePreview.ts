// 音色试听的纯逻辑：请求键、过期响应闸门、错误归类。
//
// 放在 hook 之外，是为了让"改音色后旧请求回来把新音色页面播成旧声音"这类
// 时序问题能被直接测到 —— 它在真实网络里偶发，靠手点很难复现。

export type VoicePreviewRequest = {
  voice: string;
  ratePercent: number;
  // model 是"语音模型"下拉的当前值。必须进请求键：换模型但音色名相同时，
  // 旧模型返回的样音与当前选择无关，落地就是串音。
  model?: string;
  text?: string;
};

// voicePreviewKey 是"这次试听代表什么参数"的身份。
// 文本去掉首尾空白：只有尾随空格变化不该让一次已完成的试听失效。
export function voicePreviewKey(req: VoicePreviewRequest): string {
  return [req.model ?? '', req.voice, String(req.ratePercent), (req.text ?? '').trim()].join('|');
}

export type VoicePreviewFailure = 'not_configured' | 'model_missing' | 'timeout' | 'too_long' | 'failed';

// classifyVoicePreviewError 把服务端的错误码折成五种可操作结论：
// 去配模型 / 改选模型 / 等会儿重试 / 改短文本 / 其它失败。
// 不能全塞进"试听失败"——五种情况用户要做的事完全不同。
// 特别是 model_missing：它说明模型清单里那一行已被停用或删掉，
// 用户该做的是重新选一个，而不是去检查供应商是否挂了。
export function classifyVoicePreviewError(code: string | undefined): VoicePreviewFailure {
  switch (code) {
    case 'tts_not_configured':
      return 'not_configured';
    case 'model_not_configured':
      return 'model_missing';
    case 'tts_timeout':
      return 'timeout';
    case 'text_too_long':
      return 'too_long';
    default:
      return 'failed';
  }
}

// VoicePreviewSequencer 保证"只有最新一次试听的结果能落地"。
//
// 仅用 loading 布尔量是不够的：A 还在飞时用户切到 B 并发起 B，
// 若 A 先回来就把 loading 关掉、把 A 的音频播出去，界面显示的是 B。
export class VoicePreviewSequencer {
  private seq = 0;
  private activeKey = '';

  begin(key: string): number {
    this.seq += 1;
    this.activeKey = key;
    return this.seq;
  }

  // isCurrent 同时校验 token 与参数键：
  // token 抓"又发起了一次"，参数键抓"参数变了但没重新发起"（例如只改了选择框）。
  isCurrent(token: number, key: string): boolean {
    return token === this.seq && key === this.activeKey;
  }

  // cancel 用于关闭面板/卸载：之后任何在途响应都不再落地。
  cancel(): void {
    this.seq += 1;
    this.activeKey = '';
  }
}
