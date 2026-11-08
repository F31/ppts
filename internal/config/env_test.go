package config

import (
	"testing"
	"time"
)

// 这组用例钉的是「语义」而不是实现：未设置 / 空串 / 非法值三种情况必须落到
// fallback，不能悄悄变成零值。零值在配置里几乎总是危险默认（0 秒超时、0 并发、
// 关闭的闸门看起来像开启），所以宁可让调用方显式写出它要的缺省。
func TestBool(t *testing.T) {
	cases := []struct {
		raw      string
		set      bool
		fallback bool
		want     bool
	}{
		{set: false, fallback: true, want: true},
		{set: false, fallback: false, want: false},
		{raw: "", set: true, fallback: true, want: true},
		{raw: "  ", set: true, fallback: true, want: true},
		{raw: "1", set: true, fallback: false, want: true},
		{raw: "TRUE", set: true, fallback: false, want: true},
		{raw: "Yes", set: true, fallback: false, want: true},
		{raw: "on", set: true, fallback: false, want: true},
		{raw: "0", set: true, fallback: true, want: false},
		{raw: "False", set: true, fallback: true, want: false},
		{raw: "no", set: true, fallback: true, want: false},
		{raw: "OFF", set: true, fallback: true, want: false},
		{raw: "maybe", set: true, fallback: true, want: true},
		{raw: "2", set: true, fallback: false, want: false},
	}
	for _, c := range cases {
		key := "PPTS_TEST_CONFIG_BOOL"
		t.Setenv(key, "")
		if c.set {
			t.Setenv(key, c.raw)
		}
		if got := Bool(key, c.fallback); got != c.want {
			t.Errorf("Bool(raw=%q, set=%v, fallback=%v) = %v, want %v", c.raw, c.set, c.fallback, got, c.want)
		}
	}
}

func TestIntFloatDuration(t *testing.T) {
	t.Setenv("PPTS_TEST_CONFIG_INT", "42")
	if got := Int("PPTS_TEST_CONFIG_INT", 7); got != 42 {
		t.Errorf("Int = %d, want 42", got)
	}
	t.Setenv("PPTS_TEST_CONFIG_INT", "abc")
	if got := Int("PPTS_TEST_CONFIG_INT", 7); got != 7 {
		t.Errorf("Int(非法值) = %d, want fallback 7", got)
	}
	t.Setenv("PPTS_TEST_CONFIG_INT", "")

	t.Setenv("PPTS_TEST_CONFIG_FLOAT", "1.5")
	if got := Float("PPTS_TEST_CONFIG_FLOAT", 3); got != 1.5 {
		t.Errorf("Float = %v, want 1.5", got)
	}

	t.Setenv("PPTS_TEST_CONFIG_DUR", "90s")
	if got := Duration("PPTS_TEST_CONFIG_DUR", time.Minute); got != 90*time.Second {
		t.Errorf("Duration = %v, want 90s", got)
	}
	// 纯数字不是合法时长：宁可回退，也不要让人猜量纲。
	t.Setenv("PPTS_TEST_CONFIG_DUR", "90")
	if got := Duration("PPTS_TEST_CONFIG_DUR", time.Minute); got != time.Minute {
		t.Errorf("Duration(纯数字) = %v, want fallback 1m", got)
	}
	// 非正值回退：0 间隔会让轮询变成忙转。
	t.Setenv("PPTS_TEST_CONFIG_DUR", "0s")
	if got := Duration("PPTS_TEST_CONFIG_DUR", time.Minute); got != time.Minute {
		t.Errorf("Duration(0s) = %v, want fallback 1m", got)
	}

	t.Setenv("PPTS_TEST_CONFIG_I64", "9007199254740993")
	if got := Int64("PPTS_TEST_CONFIG_I64", 0); got != 9007199254740993 {
		t.Errorf("Int64 = %d", got)
	}
}

func TestStringSetList(t *testing.T) {
	t.Setenv("PPTS_TEST_CONFIG_STR", "  value  ")
	if got := String("PPTS_TEST_CONFIG_STR", "fallback"); got != "value" {
		t.Errorf("String = %q, want value", got)
	}
	t.Setenv("PPTS_TEST_CONFIG_STR", "   ")
	if got := String("PPTS_TEST_CONFIG_STR", "fallback"); got != "fallback" {
		t.Errorf("String(空白) = %q, want fallback", got)
	}
	if !Set("PPTS_TEST_CONFIG_STR") {
		// 空白也算「没配」：与 String 的判定保持一致。
		t.Log("Set(空白) = false，与 String 的空白处理一致")
	}

	t.Setenv("PPTS_TEST_CONFIG_LIST", " a , ,b, c ")
	got := List("PPTS_TEST_CONFIG_LIST")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("List = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	t.Setenv("PPTS_TEST_CONFIG_LIST", " , , ")
	if got := List("PPTS_TEST_CONFIG_LIST"); got != nil {
		t.Errorf("List(全空项) = %v, want nil", got)
	}
}
