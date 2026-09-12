// Package integrations 承载 LLM/TTS/渲染/存储等外部适配器（V4.0 §4.2）。
// 本包及各子包是唯一允许直接接触云 SDK / 外部可执行程序的边界；
// 不包含核心业务决策。ObjectStore 端口与多后端实现见 objectstore 子包。
package integrations
