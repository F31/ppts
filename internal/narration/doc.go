// Package narration 负责讲稿、分段、来源锚点、术语规则与审核（V4.0 §4.2）。
// 保存 display_text / spoken_text / provider_payload 三份文本，维护 segment 与来源锚点。
// 不直接绑定云 SDK。
package narration
