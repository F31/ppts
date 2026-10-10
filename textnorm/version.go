package textnorm

// Version 是组件的语义化版本（V3.1 §13 兼容策略）。
//
// 兼容约定：
//   - 主版本（v1）内，公共 API 保持向后兼容；
//   - 规则/配置组合行为变化通过新增 profile 版本表达（不改变模块版本前缀）；
//   - 同一模块版本下，相同输入＋相同配置组合的输出确定且稳定（Manifest 记录）。
//
// v0.2.1：英文规则扩展（ENOrdinalRule/ENMoneyRule/ENDateRule/ENTimeRule）
// 与 en.conservative profile v2；修复 enYearToSpeech 的 2000 读法。
const Version = "v0.2.1"

// ModulePath 是独立发布的模块路径（仓库 github.com/F31/textnorm）。
const ModulePath = "github.com/F31/textnorm"
