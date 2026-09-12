// Package pipeline 负责任务依赖、状态、重试、租约与取消（V4.0 §4.2）。
// 数据库是任务事实来源，channel 只做进程内并发控制；不充当消息中间件。
package pipeline
