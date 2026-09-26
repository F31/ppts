package pipeline

import (
	"context"
	"strings"
)

// 本文件是**步骤写入规则**的唯一来源：状态覆盖规则与租约守卫各只有一份定义，
// postgres.go 与 sqlite.go 必须都从这里取 SQL，不得各写一半（参照 columns.go 的教训）。

// StepWriter 是步骤写入端口。app 侧 handler 只依赖这一个方法，
// 因此 LeaseScoped 可以直接包装 handler 声明的匿名接口。
type StepWriter interface {
	MarkStep(context.Context, JobStep) error
}

// stepStates 是 job_steps.state 的全部合法取值（与 0001_init.sql 的 CHECK 及
// 0039/sqlite 0007 追加的 degraded 一致）。新增取值必须登记在这里——
// 覆盖条件由它生成，漏登记会让新状态被当成"未下结论"而允许被 pending 抹掉。
var stepStates = []JobStepState{StepPending, StepSuccess, StepSkipped, StepFailed, StepDegraded}

// stepStateProductive 报告该状态是否代表"这一步已经产出过可供下游使用的结果"。
//
// success / skipped / degraded 都留下了结果（degraded 是降级结果，也是结果）；
// **failed 不算**——它没有产出任何东西，正因如此"重跑一个失败过的步骤"必须被允许，
// 否则任务重试时失败步骤会被永久卡在失败态。这不是疏漏：把 failed 也算进来会直接
// 让 RetryFailed 变哑弹（本文件第一个版本的测试就是这么抓到自己的）。
// pending 表示"尚未开始"，显然也不是。
func stepStateProductive(s JobStepState) bool {
	switch s {
	case StepSuccess, StepSkipped, StepDegraded:
		return true
	default:
		return false
	}
}

// stepOverwrites 决定是否允许用 next 覆盖已有状态 cur。
//
// 规则只有一条：**pending 不得抹掉"已有产出"的状态**，其余一律"后来者为准"。
// 之所以不是简单的数值单调：
//   - failed → pending 必须允许，否则任务重试时重跑失败过的步骤会被永久卡住；
//   - failed 覆盖 success 也必须允许，"这一步现在失败了"是事实，压回去才是假成功（A26）；
//   - 反过来 pending 覆盖 success 是纯粹的倒退：重跑 export/narration 步骤时会先把
//     已完成的事实抹成"未开始"，让"已生成 N/M 页"这类计数中途归零。
//
// 被拒绝的写入**静默忽略**（返回 nil，不改行），不是报错：调用方（如 export.go 开场写
// pending）把它当幂等登记用，返回错误会让一次本可成功的重跑直接失败。
func stepOverwrites(cur, next JobStepState) bool {
	if next != StepPending && next != JobStepState("") {
		return true
	}
	return !stepStateProductive(cur)
}

// stepExcludedPrefix 返回 UPSERT 里引用"待插入行"的前缀（方言差异仅此一处）。
func stepExcludedPrefix(d dialect) string {
	if d == dialectSQLite {
		return "excluded"
	}
	return "EXCLUDED"
}

// stepOverwriteCond 生成 UPSERT 的覆盖条件文本，由 stepStates + stepStateSettled 推导，
// 与 stepOverwrites 同源。两侧 SQL 都取这里，避免规则两份实现慢慢漂移。
func stepOverwriteCond(d dialect) string {
	productive := make([]string, 0, len(stepStates))
	for _, s := range stepStates {
		if stepStateProductive(s) {
			productive = append(productive, "'"+string(s)+"'")
		}
	}
	return stepExcludedPrefix(d) + ".state <> 'pending' OR job_steps.state NOT IN (" +
		strings.Join(productive, ", ") + ")"
}

// stepLeaseKey 把"当前执行者的租约凭据"放进任务执行上下文。
//
// 走 context 而不是让每个 handler 逐个填 JobStep 字段：步骤写入分散在
// app/{ingest,narration,export,scriptdraft}.go 的十几处调用点，逐个填必然漏，
// 漏掉的那处守卫就是摆设；由 worker 在执行入口注入一次，覆盖全部路径。
// 这与本包既有的 tenant.WithContext / progressKey / commitStepKey 是同一套约定。
type stepLeaseKey struct{}

type stepLease struct {
	owner   string
	fencing int64
}

// WithStepLease 声明当前上下文的任务租约凭据，供 MarkStep 校验写入者归属。
func WithStepLease(ctx context.Context, owner string, fencing int64) context.Context {
	return context.WithValue(ctx, stepLeaseKey{}, stepLease{owner: owner, fencing: fencing})
}

// resolveStepLease 决定本次写入使用的租约凭据：JobStep 上显式填写的优先，
// 其次取上下文（worker 注入），都没有则零值——零值表示不校验（历史调用方不受影响）。
func resolveStepLease(ctx context.Context, step JobStep) (string, int64) {
	if step.LeaseOwner != "" || step.FencingToken != 0 {
		return step.LeaseOwner, step.FencingToken
	}
	if l, ok := ctx.Value(stepLeaseKey{}).(stepLease); ok {
		return l.owner, l.fencing
	}
	return "", 0
}
