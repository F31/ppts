// EmptyState —— 「本来就没有数据」的统一展示（P2-C3）。
//
// 与 LoadFailure 是**两个不同状态**：本组件表达「查询成功，但结果为空」（如暂无用量明细、
// 还没有项目），不是「加载失败」。把二者渲染成同一段 UI 是 A26 明确禁止的——
// 否则用户无法区分「功能坏了」和「确实没内容」。
//
// 仅承载纯文本空态；带额外结构的空态（如 first-run 引导卡、loading 中转圈）语义不同，
// 不强行归到本组件，保持组件单一职责。
interface EmptyStateProps {
  message: string;
}

export function EmptyState({ message }: EmptyStateProps) {
  return <p className="empty-state">{message}</p>;
}
