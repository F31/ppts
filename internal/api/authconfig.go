package api

import "github.com/F31/ppts/internal/config"

// authSettings 汇总第二批的注册治理与运营商配置（环境变量驱动）。
type authSettings struct {
	// registrationEnabled=false 时 /auth/register 直接拒绝（维护期/封闭注册）。
	registrationEnabled bool
	// inviteCode 非空时，注册必须携带匹配的邀请码。
	inviteCode string
	// registerDailyPerIP 为单 IP 每日成功注册上限（0 = 不额外限制，仅用请求限流）。
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
		registrationEnabled: config.Bool("PPTS_REGISTRATION_ENABLED", true),
		inviteCode:          config.String("PPTS_REGISTRATION_INVITE_CODE", ""),
		registerDailyPerIP:  config.Int("PPTS_REGISTER_DAILY_PER_IP", defaultRegisterDailyPerIP),
		defaultGenSeconds:   config.Float("PPTS_DEFAULT_GEN_SECONDS_LIMIT", defaultTenantGenSeconds),
		priceVersion:        priceVersion,
		operatorIDs:         map[string]bool{},
	}
	if s.registerDailyPerIP < 0 {
		s.registerDailyPerIP = defaultRegisterDailyPerIP
	}
	for _, id := range config.List("PPTS_OPERATOR_USER_IDS") {
		s.operatorIDs[id] = true
	}
	return s
}

func (s authSettings) isOperator(userID string) bool {
	return userID != "" && s.operatorIDs[userID]
}

// 注册治理与配额的缺省值。集中命名而不是散在调用点：这两个数字在
// packaging/config.env.example 与运维文档里都被引用，改动时要一处改全。
const (
	// defaultRegisterDailyPerIP 是单 IP 每日成功注册上限（0 = 只受请求限流约束）。
	defaultRegisterDailyPerIP = 20
	// defaultTenantGenSeconds 是新租户默认免费配音额度（秒）。
	defaultTenantGenSeconds float64 = 600
)
