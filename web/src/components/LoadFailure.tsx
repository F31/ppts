// LoadFailure —— 数据加载失败的统一展示（P2-C3）。
//
// 此前每个页面都手写同一段 12 行的「load-failure + role=alert + 重试按钮」，
// SettingsUsage 单文件就重复了 3 处。抽成组件不是为了少打字，而是为了**让失败态长得一致**：
// 失败就是失败，必须带 role="alert"（屏幕阅读器/探测兜底都靠这个语义）、必须给出可重试入口，
// 且失败原因由调用方传入（已经是 describeApiError 拼好的「原因 + 重试」文案），
// 组件不替调用方压平不同失败原因——那正是 A26 禁止的「把故障读成同一句话」。
//
// 与 EmptyState 是两个不同的状态：本组件是「加载出错了」，EmptyState 是「本来就没数据」。
// 二者外观绝不能混用，否则「功能坏了」和「确实为空」在界面上无法区分。
interface LoadFailureProps {
  /** 失败原因文案（通常来自 describeApiError）。 */
  error: string;
  /** 提供则渲染重试按钮；不提供表示此失败不可重试（如功能未启用）。 */
  onRetry?: () => void;
  /** 重试按钮文案，默认「重试」。调用方传入以贴合当前 locale。 */
  retryLabel?: string;
}

export function LoadFailure({ error, onRetry, retryLabel }: LoadFailureProps) {
  return (
    <div className="load-failure" role="alert">
      <p className="form-error">{error}</p>
      {onRetry && (
        <button type="button" onClick={() => void onRetry()}>
          {retryLabel ?? '重试'}
        </button>
      )}
    </div>
  );
}
