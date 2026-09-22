-- job_steps.state 新增取值 degraded（M9）。
--
-- 背景：一键成稿时，数字/单位/型号校验两轮仍不过，draftText 会回退为「原始素材当讲稿落库」。
-- 该结果必须能与正常生成区分：否则任务步骤记 success、界面报「成稿完成：共 N 页」，用户无法
-- 分辨自己看到的是讲解稿还是页面要点片段（违反 A26「不得假成功」）。
--
-- 约束改名方式说明：原约束是匿名 CHECK（见 0001_init.sql），PostgreSQL 默认命名为
-- job_steps_state_check。此处**按定义动态匹配**而非硬编码名字——若名字与预期不符，
-- `DROP CONSTRAINT IF EXISTS <猜的名字>` 会静默跳过，随后的 ADD 只是又加了一个约束，
-- 旧约束仍在并继续拒绝 degraded：迁移"成功"但写入失败，属于最难排查的一类问题。
--
-- 先 SELECT 到数组再循环 DROP：`FOR ... IN SELECT ... FROM pg_constraint` 是「边扫描系统表
-- 边修改它」，PL/pgSQL 游标行为在循环体内发生 DDL 时不可预测，故不采用。
DO $$
DECLARE
  names text[];
  n     text;
BEGIN
  SELECT coalesce(array_agg(conname), '{}')
    INTO names
    FROM pg_constraint
   WHERE conrelid = 'job_steps'::regclass
     AND contype = 'c'
     AND pg_get_constraintdef(oid) LIKE '%state%';

  FOREACH n IN ARRAY names
  LOOP
    EXECUTE format('ALTER TABLE job_steps DROP CONSTRAINT %I', n);
  END LOOP;
END
$$;

ALTER TABLE job_steps
  ADD CONSTRAINT job_steps_state_check
    CHECK (state IN ('pending', 'success', 'skipped', 'failed', 'degraded'));
