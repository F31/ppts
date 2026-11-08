package pipeline

import (
	"testing"
	"time"
)

// nullableRunAt 是唯一把「零值 = 立即可领取」翻译成 NULL 的地方。
// 这条断言不需要数据库，因此每次 go test 都会跑：PG 集成测试需要
// PPTS_TEST_DATABASE，本地默认跳过，零值回归曾因此长期无人发现。
func TestNullableRunAt(t *testing.T) {
	if got := nullableRunAt(time.Time{}); got != nil {
		t.Fatalf("零值必须落成 NULL，实际得到 %#v", got)
	}
	at := time.Date(2026, 11, 8, 12, 30, 0, 0, time.UTC)
	if got := nullableRunAt(at); got != any(at) {
		t.Fatalf("非零值必须原样传出：got %#v want %v", got, at)
	}
}
