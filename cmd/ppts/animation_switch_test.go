package main

import "testing"

// TestAnimationEnabled 守「api 与 worker 必须同一份判定」这条线。
//
// 两处历史实现是 api `isSQLite() || env=="true"`、worker `env=="true"`：
// 本地 profile 下 api 报"能出动画"、worker 却只出静态版，用户看到的是静默降级。
// 更糟的是本地 profile 根本关不掉——4.4「关掉开关再导出」无从执行。
func TestAnimationEnabled(t *testing.T) {
	cases := []struct {
		name     string
		env      string // "" = 未设置
		isSQLite bool
		want     bool
	}{
		{"本地默认开", "", true, true},
		{"生产默认关", "", false, false},
		{"显式开（生产）", "true", false, true},
		{"显式关（本地）", "false", true, false},
		{"显式关（生产）", "false", false, false},
		{"非法值回落到 profile", "maybe", false, false},
		{"非法值回落（本地仍开）", "maybe", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env == "" {
				t.Setenv("PPTS_ANIMATION_ENABLED", "")
			} else {
				t.Setenv("PPTS_ANIMATION_ENABLED", tc.env)
			}
			if got := animationEnabled(tc.isSQLite); got != tc.want {
				t.Fatalf("animationEnabled(%v) with env=%q = %v, want %v", tc.isSQLite, tc.env, got, tc.want)
			}
		})
	}
}
