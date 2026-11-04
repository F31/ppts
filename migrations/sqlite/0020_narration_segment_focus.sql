-- 讲稿分段的画面焦点（与 pg 0053 对齐，2026-11-04）
--
-- SQLite profile 用 TEXT 存 JSON 对象：单租户形态没有 jsonb 类型，读取时由调用方
-- 反序列化。NULL = 这段不指任何东西（与 PG 语义保持一致，不是 "{}"）。
--
-- 为什么加列而不是另起一张表：焦点与分段是 1:1 且总是一起读写（整页替换分段时
-- 一起删/插），另起一张表只会让"替换分段"变成两次删除、两处可能半途而废。
ALTER TABLE narration_segments ADD COLUMN focus TEXT;
