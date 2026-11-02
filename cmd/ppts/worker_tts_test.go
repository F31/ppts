package main

import (
	"bytes"
	"encoding/base64"
	"io"
	"log/slog"
	"strings"
	"testing"

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
