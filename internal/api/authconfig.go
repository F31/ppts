package api

import (
	"os"
	"strconv"
	"strings"
)

// authSettings 汇总第二批的注册治理与运营商配置（环境变量驱动）。
type authSettings struct {
	// registrationEnabled=false 时 /auth/register 直接拒绝（维护期/封闭注册）。
	registrationEnabled bool
	// inviteCode 非空时，注册必须携带匹配的邀请码。
	inviteCode string
	// registerDailyPerIP 为单 IP 每日注册上限（0 = 不额外限制，仅用每小时限流）。
	registerDailyPerIP int
	// defaultGenSeconds 为新租户发放的默认免费配音额度（秒）；<0 表示不限量、=0 表示不发放。
	defaultGenSeconds float64
	// priceVersion 写入默认配额行的定价版本（可空）。
	priceVersion string
	// operatorIDs 是运营商用户 ID 集合（PPTS_OPERATOR_USER_IDS，逗号分隔）。
	operatorIDs map[string]bool
}

// authSettingsFromEnv 读取注册治理/运营商配置（缺省值面向"可安全开放注册"）。
func authSettingsFromEnv(priceVersion string) authSettings {
	s := authSettings{
		registrationEnabled: envBoolValue("PPTS_REGISTRATION_ENABLED", true),
		inviteCode:          strings.TrimSpace(os.Getenv("PPTS_REGISTRATION_INVITE_CODE")),
		registerDailyPerIP:  envIntValue("PPTS_REGISTER_DAILY_PER_IP", 20),
		defaultGenSeconds:   envFloatValue("PPTS_DEFAULT_GEN_SECONDS_LIMIT", 600),
		priceVersion:        priceVersion,
		operatorIDs:         map[string]bool{},
	}
	for _, id := range strings.Split(os.Getenv("PPTS_OPERATOR_USER_IDS"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			s.operatorIDs[id] = true
		}
	}
	return s
}

func (s authSettings) isOperator(userID string) bool {
	return userID != "" && s.operatorIDs[userID]
}

func envIntValue(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func envFloatValue(key string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return f
}
