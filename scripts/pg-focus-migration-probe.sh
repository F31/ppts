#!/usr/bin/env bash
# 部署前演练：0053（narration_segments.focus）在「有老讲稿的存量库」上加列。
#
# 为什么单独留这个脚本：CI 的 postgres job 是空库上逐文件 psql -f，验的是「迁移能不能跑」；
# 部署时跑的是 `ppts migrate`（内嵌迁移 + ppts_schema_migrations 版本表），面对的是
# 「旧版本已经上线、库里有讲稿」的库。两条路的执行器和起点都不一样。
# 更关键的是，旧讲稿读回来必须是「这段不指任何东西」，而不是指向一个空形状——
# 这一条只有在真的有旧行时才验得到。
#
# 造旧库不用手搭 tracking 表：把 0053 临时移出 *.sql，用产品自己的 `ppts migrate`
# 跑一遍「旧版本」，插老讲稿，再把文件放回去跑「升级」。两次都是同一条部署命令。
#
# 用法（需要 docker + go；Windows 请在 WSL 里执行本脚本）：
#   bash scripts/pg-focus-migration-probe.sh
#   GO_BIN=~/.local/go/bin/go bash scripts/pg-focus-migration-probe.sh
# 产物只有终端输出；容器默认自动清理，KEEP_CONTAINER=1 可留下来排查。
set -euo pipefail

cd "$(dirname "$0")/.."
REPO=$(pwd)
FOCUS=migrations/0053_narration_segment_focus.sql
NAME=${NAME:-ppts-pg-migrate-probe}
PORT=${PORT:-55433}
DSN_ADMIN="postgres://postgres:postgres@localhost:${PORT}/ppts_test?sslmode=disable"
DSN_APP="postgres://ppts_app:ppts_app@localhost:${PORT}/ppts_test?sslmode=disable"

if [ -n "${GO_BIN:-}" ]; then GO="$GO_BIN"
elif command -v go >/dev/null 2>&1; then GO=go
elif [ -x "$HOME/.local/go/bin/go" ]; then GO="$HOME/.local/go/bin/go"
else echo "找不到 go：请设 GO_BIN=/path/to/go" >&2; exit 2; fi

# -i 不能省：第 3 步的 heredoc 是经 stdin 喂给容器内 psql 的。
psql_admin() { docker exec -i -e PGPASSWORD=postgres "$NAME" psql -U postgres -d ppts_test -v ON_ERROR_STOP=1 -tAq "$@"; }

cleanup() {
  if [ -f "${FOCUS}.probe-bak" ]; then mv "${FOCUS}.probe-bak" "$FOCUS"; fi
  if [ "${KEEP_CONTAINER:-0}" != 1 ]; then docker rm -f "$NAME" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

echo "== [1/7] 起一个干净的 PostgreSQL =="
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" \
  -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=ppts_test \
  -p "${PORT}:5432" postgres:16-alpine >/dev/null
ready=0
for i in $(seq 1 60); do
  if docker exec "$NAME" pg_isready -U postgres -d ppts_test >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
[ "$ready" = 1 ] || { echo "PostgreSQL 没就绪" >&2; exit 2; }
# 与 CI prepare database 一致：先建运行账号，迁移里的 IF NOT EXISTS 才会跳过。
psql_admin -c "CREATE ROLE ppts_app LOGIN PASSWORD 'ppts_app' NOSUPERUSER NOBYPASSRLS;" >/dev/null

echo "== [2/7] 用旧版本 ppts migrate 造存量库（0053 尚未发布）=="
mv "$FOCUS" "${FOCUS}.probe-bak"
old_log=$(PPTS_MIGRATE_DATABASE_URL="$DSN_ADMIN" "$GO" run ./cmd/ppts migrate 2>&1)
mv "${FOCUS}.probe-bak" "$FOCUS"
old_count=$(printf '%s\n' "$old_log" | grep -c "^migrate: applied " || true)
echo "    旧版本应用 $old_count 条迁移；schema_migrations 记录 $(psql_admin -c 'SELECT count(*) FROM ppts_schema_migrations') 条"
[ "$old_count" -gt 0 ] || { echo "旧版本没有应用任何迁移" >&2; printf '%s\n' "$old_log"; exit 1; }
if [ "$(psql_admin -c "SELECT count(*) FROM information_schema.columns WHERE table_name='narration_segments' AND column_name='focus'")" != 0 ]; then
  echo "起点错误：旧版本库里已经有 focus 列" >&2; exit 1
fi

echo "== [3/7] 写一份老讲稿（两个分段，都没有焦点）=="
psql_admin <<'SQL' >/dev/null
INSERT INTO narration_scripts (id, tenant_id, project_id, slide_id, language)
VALUES ('11111111-1111-1111-1111-111111111111',
        '22222222-2222-2222-2222-222222222222',
        '33333333-3333-3333-3333-333333333333', 'slide-01', 'zh-CN');
INSERT INTO narration_segments (id, script_id, tenant_id, segment_id, display_text, spoken_text)
VALUES ('44444444-4444-4444-4444-444444444444',
        '11111111-1111-1111-1111-111111111111',
        '22222222-2222-2222-2222-222222222222', 'seg-01', '第一段', '第一段'),
       ('55555555-5555-5555-5555-555555555555',
        '11111111-1111-1111-1111-111111111111',
        '22222222-2222-2222-2222-222222222222', 'seg-02', '第二段', '第二段');
SQL

echo "== [4/7] 跑部署命令 ppts migrate（本次应只补 0053）=="
new_log=$(PPTS_MIGRATE_DATABASE_URL="$DSN_ADMIN" "$GO" run ./cmd/ppts migrate 2>&1)
printf '%s\n' "$new_log" | grep "^migrate: applied " || true
applied=$(printf '%s\n' "$new_log" | grep "^migrate: applied " || true)
if [ "$applied" != "migrate: applied 0053_narration_segment_focus.sql" ]; then
  echo "升级应且只应应用 0053，实得：$applied" >&2; printf '%s\n' "$new_log" | tail -20; exit 1
fi

echo "== [5/7] 断言老讲稿还在，且 focus 是 SQL NULL 而不是 jsonb 的 'null' / '{}' =="
rows=$(psql_admin -c "SELECT count(*) FROM narration_segments")
nulls=$(psql_admin -c "SELECT count(*) FROM narration_segments WHERE focus IS NULL")
notnull=$(psql_admin -c "SELECT count(*) FROM narration_segments WHERE focus IS NOT NULL")
typ=$(psql_admin -c "SELECT data_type FROM information_schema.columns WHERE table_name='narration_segments' AND column_name='focus'")
echo "    rows=$rows nulls=$nulls notNull=$notnull type=$typ"
[ "$rows" = 2 ] && [ "$nulls" = 2 ] && [ "$notnull" = 0 ] && [ "$typ" = jsonb ] || {
  echo "老讲稿被迁移改坏了" >&2; exit 1; }

echo "== [6/7] 部署重跑应是空操作（幂等）=="
again=$(PPTS_MIGRATE_DATABASE_URL="$DSN_ADMIN" "$GO" run ./cmd/ppts migrate 2>&1)
printf '%s\n' "$again" | grep "migrate: ok" || true
if ! printf '%s\n' "$again" | grep -q "migrate: ok, 0 applied"; then
  echo "重复部署不是空操作：$again" >&2; exit 1
fi

echo "== [7/7] 同一份判据在真 PG 上再跑一次（-tags=pg，CI postgres job 用的就是它）=="
PPTS_TEST_DATABASE="$DSN_APP" "$GO" test -tags=pg ./internal/narration/ \
  -run TestPGFocusMigrationKeepsExistingRows -count=1 -v 2>&1 | tail -6

echo "PG_FOCUS_MIGRATION_PROBE_PASS"
