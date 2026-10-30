package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/usage"
)

// 动画渲染额度（M4 §7.5）。
//
// 语义定清楚，因为这里的每个选择都会在线上变成一个具体的用户行为：
//
//  1. **按账期计**：账期（UTC 月）编进计量种类，跨月是额度表里的新行。
//     额度预占靠 tenant_quotas 的单行条件更新保证原子（并发不超卖），
//     而"跨月清零"如果做成字段比较，就必须在事务外先读后写，那就不再原子了。
//  2. **受理时预占**：超限必须在**提交导出时**明确拒绝，不能先建任务再在 worker 里失败——
//     那就是设计 §7.5 明确禁止的"静默排队"：用户以为在渲染，等十分钟得到一条失败。
//  3. **按真实时长结算**：结算用「真的渲了动画的页」的时长，降级成静态段的页不收费。
//     按整片时长收，等于在渲染服务出问题（降级最多）的时候按满额收费。
//  4. **默认不限**：`animation.max_minutes` 未配置时不设上限（见 design ADR-7）。
//     升级当天给所有存量租户加一道硬停，与 ADR-3 拒绝翻转灰度默认值是同一条理由。

// animationQuota 是额度受理侧（预占与拒绝）。结算在 worker 的导出处理器里做，
// 因为只有那里知道"哪几页真的渲了动画"。
type animationQuota struct {
	store   usage.Store
	objects objectstore.ObjectStore
	logger  *slog.Logger
}

// quotaExhaustedError 是"这次导出超出动画额度"的判定结果。
//
// 单独成类型而不是一个 bool：拒绝的响应要能回答"上限多少、已用多少、什么时候恢复"，
// 只回一句 quota exceeded 会让用户与支持都不知道下一步该做什么。
type quotaExhaustedError struct {
	Period    string
	Limit     float64
	Used      float64
	Reserved  float64
	Requested float64
	ResetAt   time.Time
}

func (e *quotaExhaustedError) Error() string {
	return "animation quota exceeded for " + e.Period
}

// Reserve 为一次导出预占渲染分钟数，返回预占记录与预占量。
//
// 返回 (nil, 0, nil) 表示"这次不参与额度管理"（没有账本，或时间轴读不出来）。
// 后者刻意不是错误：时间轴读不出来时导出本身会在 worker 里失败，
// 为一次注定失败的导出扣额度（或在受理时因额度报错）都只会误导用户。
func (q *animationQuota) Reserve(ctx context.Context, tenantID, opID, timelineKey string, limitMinutes float64) (*usage.Reservation, float64, error) {
	if q == nil || q.store == nil || tenantID == "" || opID == "" {
		return nil, 0, nil
	}
	minutes, ok := q.estimateMinutes(ctx, tenantID, timelineKey)
	if !ok {
		return nil, 0, nil
	}
	period := usage.MonthPeriod(time.Now())
	kind := usage.KindAnimationMinutes.Period(period)

	// 上限来自租户策略，写进额度表后才对 Reserve 生效。SetLimit 是幂等 upsert：
	// 同一个账期内重复写同一个值没有副作用，因此不需要"是否已配置"的判断——
	// 那个判断本身会引入一次读改写。
	//
	// **每次都写，包括不限量（-1）**：策略是上限的唯一来源，受理时同步一次，
	// 运营把 max_minutes 从 120 改成 0 才会在下一次导出时立刻生效。
	// 只在"配了上限"时才写的话，改回不限量会留下上一版的 120，变成一个查不到的暗坑。
	storedLimit := -1.0
	if limitMinutes > 0 {
		storedLimit = limitMinutes
	}
	if err := q.store.SetLimit(ctx, tenantID, kind, storedLimit, ""); err != nil {
		return nil, 0, err
	}
	res, err := q.store.Reserve(ctx, tenantID, opID, kind, minutes)
	if errors.Is(err, usage.ErrInsufficientQuota) {
		quota, qerr := q.store.GetQuota(ctx, tenantID, kind)
		if qerr != nil || quota == nil {
			return nil, 0, err
		}
		reset, rerr := usage.MonthReset(period)
		if rerr != nil {
			reset = time.Time{}
		}
		return nil, 0, &quotaExhaustedError{
			Period: period, Limit: quota.LimitUnits,
			Used: quota.ConsumedUnits, Reserved: quota.ReservedUnits,
			Requested: minutes, ResetAt: reset,
		}
	}
	if err != nil {
		return nil, 0, err
	}
	return res, minutes, nil
}

// estimateMinutes 预估本次导出会消耗的渲染分钟数。
//
// 用时间轴总时长而不是"页数 × 常数"：渲染器按页时长出片段，成片时长就是时间轴时长，
// 所以这是**精确估计**而不是上界。降级页会在结算时扣回来，预占略高于实际是安全的。
func (q *animationQuota) estimateMinutes(ctx context.Context, tenantID, timelineKey string) (float64, bool) {
	seconds, err := timelineSeconds(ctx, q.objects, tenantID, timelineKey)
	if err != nil {
		if q.logger != nil {
			// 读不出时间轴不是"额度问题"，别让日志指向额度。
			q.logger.Warn("animation quota: timeline unreadable; skipping reservation",
				"tenant_id", tenantID, "error", err)
		}
		return 0, false
	}
	if seconds <= 0 {
		return 0, true
	}
	return seconds / 60.0, true
}

// writeQuotaExceeded 把额度拒绝写成 429。
//
// 响应体里必须有"上限/已用/本次需要/恢复时间"四个数：只回一句 quota exceeded，
// 用户不知道是这次片子太长、还是这个月用完了；支持也不知道该不该给额度。
func writeQuotaExceeded(w http.ResponseWriter, e *quotaExhaustedError) {
	body := map[string]any{
		"code":             "animation_quota_exceeded",
		"message":          fmt.Sprintf("animation render quota for %s is exhausted", e.Period),
		"period":           e.Period,
		"limitMinutes":     e.Limit,
		"usedMinutes":      e.Used,
		"reservedMinutes":  e.Reserved,
		"requestedMinutes": e.Requested,
	}
	if !e.ResetAt.IsZero() {
		// 恢复时间是账期边界（UTC 下月 1 日 00:00）：给绝对时刻而不是"等 N 天"，
		// 前端与会话、工单之间的时间基准不一致时，"还剩 3 天"会被算错。
		body["resetAt"] = e.ResetAt.UTC().Format(time.RFC3339)
		// Retry-After 让通用错误处理（core.ts 的 ConnectError）无需解析业务字段
		// 也能算出恢复时间——前端文案只需要一个相对秒数。
		if secs := int(time.Until(e.ResetAt).Seconds()); secs > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(secs))
		} else {
			w.Header().Set("Retry-After", "1")
		}
	}
	writeJSON(w, http.StatusTooManyRequests, body)
}

// timelineSeconds 读时间轴 JSON 的总时长（秒）。
// 不复用 loadBundle：那个函数还要求 SRT/VTT 键齐全，而额度只关心时长，
// 用更严的条件做预估会让"字幕还没生成"的时间轴莫名其妙地不受额度约束。
func timelineSeconds(ctx context.Context, objects objectstore.ObjectStore, tenantID, rawKey string) (float64, error) {
	if objects == nil {
		return 0, errors.New("object store is not configured")
	}
	key, err := objectstore.Parse(rawKey)
	if err != nil {
		return 0, err
	}
	if err := key.EnsureTenant(tenantID); err != nil {
		return 0, err
	}
	r, _, err := objects.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, 8<<20))
	if err != nil {
		return 0, err
	}
	var bundle app.TimelineAsset
	if err := json.Unmarshal(data, &bundle); err != nil {
		return 0, err
	}
	if bundle.Timeline == nil {
		return 0, errors.New("timeline bundle has no timeline")
	}
	if us := bundle.Timeline.DurationUS; us > 0 {
		return float64(us) / 1e6, nil
	}
	// 历史时间轴可能没写 durationUs：按页累加，仍是精确值。
	var totalUS int64
	for _, slide := range bundle.Timeline.Slides {
		if slide.EndUS > slide.StartUS {
			totalUS += slide.EndUS - slide.StartUS
		}
	}
	return float64(totalUS) / 1e6, nil
}
