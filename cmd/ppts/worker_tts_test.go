package main

import (
	"bytes"
	"encoding/base64"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/integrations/tts"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestTTSProviderFromEnvUnsetMeansNoEnvProvider 守"空值 ≠ 配置缺失"这条线。
//
// 修前：PPTS_TTS_PROVIDER 未设置直接启动失败（unsupported PPTS_TTS_PROVIDER=""）。
// 但模型网关是产品主路径——用户在 Web 里配 TTS，DB 行里就有 key，根本不需要这个变量。
// 纯网关部署因此永远起不来，而报错指向一个用户从没见过的环境变量。
func TestTTSProviderFromEnvUnsetMeansNoEnvProvider(t *testing.T) {
	t.Setenv("PPTS_TTS_PROVIDER", "")
	provider, err := ttsProviderFromEnv(discardLogger())
	if err != nil {
		t.Fatalf("未设置 PPTS_TTS_PROVIDER 应返回 (nil,nil)，实际报错：%v", err)
	}
	if provider != nil {
		t.Fatalf("未设置时应返回 nil 供应商（交由网关解析），实际得到 %T", provider)
	}
}

// TestTTSProviderFromEnvRejectsUnknownValue 守"拼错必须失败"这条线。
//
// 允许空值不等于允许拼错：把 fake 当兜底会让配音任务 succeeded 但成片无声，
// 且 ppts_tts_synthesis_total 计成功——连指标都是假的。
func TestTTSProviderFromEnvRejectsUnknownValue(t *testing.T) {
	t.Setenv("PPTS_TTS_PROVIDER", "openai")
	_, err := ttsProviderFromEnv(discardLogger())
	if err == nil {
		t.Fatal("未知供应商必须启动失败，不能静默回落 fake")
	}
	if !strings.Contains(err.Error(), "unsupported PPTS_TTS_PROVIDER") {
		t.Fatalf("错误信息应指明变量名，实际：%v", err)
	}
}

// TestTTSFallback 守"配了但没设默认"这条线。
//
// 修前：Resolve 返回 ErrNotFound 时一律回退 env 供应商。而 ErrNotFound 同时表示
// "从没配过"和"配了但没标默认"——后者的典型现场是：用户在设置页配好 TTS、点测试
// 连接通过（那一步按名字探活，与默认无关），导出却拿到 env 的 fake 静音轨，任务还是
// succeeded。日志里没有任何一行能看出用了谁。
func TestTTSFallback(t *testing.T) {
	const tenant = "11111111-1111-1111-1111-111111111111"

	t.Run("有网关但未启用或未设默认必须报错", func(t *testing.T) {
		rows := []*gateway.Gateway{
			{Name: "siliconflow", Kind: gateway.KindTTS, Enabled: true, IsDefault: false},
			{Name: "backup", Kind: gateway.KindTTS, Enabled: false, IsDefault: true},
		}
		_, err := ttsFallback(tenant, rows, tts.NewFakeProvider())
		if err == nil {
			t.Fatal("有 TTS 行却都不处于「启用 + 默认」时必须失败，不能回退 env 静默合成")
		}
		for _, want := range []string{"siliconflow", "backup(已停用)", "设置 → 模型服务", "不会回退到环境变量供应商"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("错误信息应包含 %q，实际：%v", want, err)
			}
		}
	})

	t.Run("没有任何 TTS 行才允许回退 env", func(t *testing.T) {
		env := tts.NewFakeProvider()
		got, err := ttsFallback(tenant, nil, env)
		if err != nil {
			t.Fatalf("无 TTS 行时应回退 env 供应商，实际报错：%v", err)
		}
		if got != env {
			t.Fatalf("应原样返回 env 供应商，实际得到 %T", got)
		}
	})

	t.Run("既无行也无 env 供应商必须报错", func(t *testing.T) {
		_, err := ttsFallback(tenant, nil, nil)
		if err == nil {
			t.Fatal("两条路都没有时必须失败")
		}
		if !strings.Contains(err.Error(), "no TTS provider configured") {
			t.Fatalf("错误信息应指出两条路都缺，实际：%v", err)
		}
	})
}

// TestRequireTTSProvider 守启动期"至少有一条路可用"这条线。
func TestRequireTTSProvider(t *testing.T) {
	aesKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))

	t.Run("有 env 供应商即可启动", func(t *testing.T) {
		t.Setenv("PPTS_GATEWAY_AES_KEY_BASE64", "")
		t.Setenv("PPTS_BYOS_AES_KEY_BASE64", "")
		if err := requireTTSProvider(tts.NewFakeProvider()); err != nil {
			t.Fatalf("有 env 供应商时不应报错：%v", err)
		}
	})

	t.Run("无 env 且无网关密钥必须失败", func(t *testing.T) {
		t.Setenv("PPTS_GATEWAY_AES_KEY_BASE64", "")
		t.Setenv("PPTS_BYOS_AES_KEY_BASE64", "")
		err := requireTTSProvider(nil)
		if err == nil {
			t.Fatal("env 与网关都没有时必须启动失败")
		}
		if !strings.Contains(err.Error(), "PPTS_GATEWAY_AES_KEY_BASE64") {
			t.Fatalf("错误信息应指出网关密钥这条路径，实际：%v", err)
		}
	})

	t.Run("无 env 但有网关密钥可启动", func(t *testing.T) {
		t.Setenv("PPTS_GATEWAY_AES_KEY_BASE64", aesKey)
		// 具体有没有 TTS 行要按租户在任务执行时解析，启动期无法也不该替所有租户判断。
		if err := requireTTSProvider(nil); err != nil {
			t.Fatalf("配了网关密钥就应允许启动：%v", err)
		}
	})
}
