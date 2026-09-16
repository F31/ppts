-- 0026_jobs_phase_scope.sql
-- 目标（B4-M6b）：为任务列表的「阶段」筛选/排序与「受影响页数」排序提供**可索引**的持久化列。
--
-- 口径（定案见 docs/PPT智能语音讲解平台-控制台产品优化实施计划-V1.0.md §B4-M6b）：
--   * jobs.phase = 任务当前阶段 = 最近更新的 job_steps.step_type（取值：pages / tts_segment /
--     timeline / export，见 internal/app/*.go 的 MarkStep 调用）。由 pipeline.PGStore.MarkStep
--     在与步骤写入**同一事务**的 UPDATE jobs SET ... 中一并维护，因此不存在“步骤已变、阶段未变”
--     的漂移；其权威来源始终是 job_steps，本列只是它的可索引投影。
--   * jobs.affected_pages = 入队时从 input_snapshot 提取的受影响页/段 ID 列表（jsonb 数组）。
--     由 pipeline.PGStore.Create 写入；取不到时为空数组（不猜测）。
--
-- 为什么 affected_pages 必须落列：input_snapshot 是 text，且页列表字段名随 kind 变化
-- （narration: slides[].slideId 或 segmentIds；script_draft: slideIds），**无法在 SQL 内解析**，
-- 因此「按受影响页数排序」这一能力只能由本列支撑（快照不是可查询的等价来源）。
--
-- 注意：本迁移**不**修改 jobSelectColumns 对应的核心投影，也**不**重建 ppts_claim_next_job
-- （该函数在 0018 已重建过；见 0018 的教训——把列加进核心投影会连带要求重建调度函数）。
-- phase/affected_pages 是控制台查询列，仅由 pipeline 的列表查询按需 SELECT。

ALTER TABLE jobs ADD COLUMN IF NOT EXISTS phase text NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS affected_pages jsonb NOT NULL DEFAULT '[]'::jsonb;

-- 回填 phase：每个任务最近更新的步骤类型（与运行期 MarkStep 的写入口径一致；仅填空白，可重复执行）。
UPDATE jobs j
   SET phase = s.step_type
  FROM (
        SELECT DISTINCT ON (job_id) job_id, step_type
          FROM job_steps
         ORDER BY job_id, updated_at DESC, step_type
       ) s
 WHERE s.job_id = j.id
   AND j.phase = '';

-- 回填 affected_pages：仅对“看起来是 JSON 对象”的快照解析（input_snapshot 为 text 且默认 ''，
-- 直接 ::jsonb 会报错，故先用正则保护）；按 kind 取对应字段，取不到一律留空数组，不猜测。
-- 每种取值路径都用 jsonb_typeof 守卫，避免 jsonb_array_elements 在非数组上抛错。
UPDATE jobs
   SET affected_pages = CASE kind
         -- narration：受影响页始终取 slides[].slideId（segmentIds 非空只让 kind 变 'segments'，
         -- 不改变页列表；见 pipeline.ScopeOf 的实现，必须与它逐字一致，否则范围列与页数排序会互相矛盾）。
         WHEN 'narration' THEN
           CASE
             WHEN jsonb_typeof(input_snapshot::jsonb -> 'slides') = 'array'
             THEN COALESCE((
                    SELECT jsonb_agg(elem ->> 'slideId')
                      FROM jsonb_array_elements(input_snapshot::jsonb -> 'slides') AS elem
                     WHERE elem ->> 'slideId' IS NOT NULL
                  ), '[]'::jsonb)
             ELSE '[]'::jsonb
           END
         WHEN 'script_draft' THEN
           CASE
             WHEN jsonb_typeof(input_snapshot::jsonb -> 'slideIds') = 'array'
             THEN input_snapshot::jsonb -> 'slideIds'
             ELSE '[]'::jsonb
           END
         ELSE '[]'::jsonb
       END
 WHERE input_snapshot ~ '^[[:space:]]*\{'
   AND affected_pages = '[]'::jsonb;

-- 阶段筛选 + 按阶段排序；created_at DESC 供默认排序复用同一索引前缀。
CREATE INDEX IF NOT EXISTS idx_jobs_phase ON jobs(tenant_id, phase, created_at DESC);

-- 受影响页数排序（表达式索引；jsonb_array_length 无法直接用普通索引）。
CREATE INDEX IF NOT EXISTS idx_jobs_pages ON jobs(tenant_id, (jsonb_array_length(affected_pages)));
