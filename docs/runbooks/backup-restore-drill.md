# Backup/Restore Drill

G3-4 恢复目标：HA 版暂定 RPO <= 15 分钟、RTO <= 2 小时。该演练验证数据库备份可恢复，并可选验证本地对象存储中 `object_inventory.object_key` 引用仍存在。

## Scope

- 数据库：`pg_dump --format=custom` 备份，`pg_restore` 恢复到独立演练库。
- 对象：当前 local store 可通过 `OBJECT_ROOT` 校验引用存在；S3/BYOS 需在生产 runbook 中替换为对应 bucket inventory 或对象 HEAD 批量校验。
- 不覆盖：备份调度器、WAL/PITR 托管配置、跨区域复制 SLA。

## Local Drill

1. 创建独立目标库，例如 `ppts_restore_drill`。
2. 确认目标库为空，禁止指向生产/开发主库。
3. 执行：

```bash
SOURCE_DATABASE_URL="postgres://.../ppts_test" \
RESTORE_DATABASE_URL="postgres://.../ppts_restore_drill" \
OBJECT_ROOT="/path/to/local-object-root" \
BACKUP_INTERVAL_SECONDS=900 \
scripts/backup_restore_drill.sh
```

脚本输出 report 路径，报告记录备份耗时、恢复耗时、关键表计数、RPO/RTO 判断和对象引用检查结果。

## Acceptance

- `rpo_target_15m: PASS`：备份调度间隔不超过 900 秒。
- `rto_target_2h: PASS`：恢复耗时不超过 7200 秒。
- `object_reference_check: PASS`：设置 `OBJECT_ROOT` 时，恢复库内对象清单引用全部能在对象根目录找到。

## Notes

- RPO 与“不丢进程内任务”不是同一故障范围；数据库灾难可能丢失备份窗口内数据。
- 演练库恢复完成后，应用层仍需用普通 `ppts_app` 账号跑 RLS/关键 API 冒烟，确认运行角色不是 owner 且不具备 `BYPASSRLS`。
