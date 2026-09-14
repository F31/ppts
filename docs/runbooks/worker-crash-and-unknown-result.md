# Worker Crash And Unknown Result Runbook

G3-5 目标：worker 崩溃、对象写失败、供应商结果未知时，不能重复扣费、不能产生伪成功，任务必须可观测并可恢复。

## Worker Crash

- 任务领取后写入 `lease_owner`、`lease_until`、`fencing_token`。
- worker 停机或进程崩溃时不提交终态；租约过期后，新 worker 通过 `ClaimNext/ClaimNextAny` 重领取同一任务，`attempt` 与 `fencing_token` 递增。
- 过期 worker 的 `Complete/ScheduleRetry` 因 fencing 不匹配返回 `ErrLeaseMismatch`，不会覆盖新结果。
- 已成功写入的 `job_steps` 使用 `(job_id, step_key)` 幂等重放，重试不会生成重复步骤。

## Object Write Failure

- handler 必须先写对象，再创建 artifact/账本等业务记录。
- 对象写失败（磁盘满、S3 5xx、权限错误）返回错误并标记 step failed，不创建 artifact，不提交成功终态。
- 普通错误最终进入 `failed`；可重试错误返回 `RetryError` 进入 `retry_wait`。

## Unknown Provider Result

- 当供应商请求已经发出，但网络中断/超时导致结果不可确认时，handler 返回 `pipeline.UnknownResultError`。
- worker 将任务置为 `unknown_provider_result` 并保存 `last_error`，不自动继续执行，避免重复请求供应商造成重复扣费或重复生成。
- 对账确认未执行或结果可安全重试后，可调用 `JobService.RetryFailed` 将任务重新入队；该接口同时支持 `failed` 与 `unknown_provider_result`。

## Test Coverage

- `TestWorkerCrashReclaimNoLoss`
- `TestWorkerCrashAfterStepReplayIdempotent`
- `TestWorkerUnknownProviderResultCanBeRetried`
- `TestExportHandlerObjectWriteFailureDoesNotCreateArtifact`
