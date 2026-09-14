#!/usr/bin/env bash
set -euo pipefail

# G3-4 backup/restore drill helper.
# Required:
#   SOURCE_DATABASE_URL  source database DSN
#   RESTORE_DATABASE_URL empty drill target database DSN
# Optional:
#   OBJECT_ROOT          local object store root; validates object_inventory keys exist under it
#   BACKUP_INTERVAL_SECONDS expected backup cadence, default 900 (15 min RPO target)
#   WORKDIR              artifact directory, default mktemp

require() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'missing required command: %s\n' "$1" >&2
    exit 127
  fi
}

require pg_dump
require pg_restore
require psql

: "${SOURCE_DATABASE_URL:?SOURCE_DATABASE_URL is required}"
: "${RESTORE_DATABASE_URL:?RESTORE_DATABASE_URL is required}"

backup_interval="${BACKUP_INTERVAL_SECONDS:-900}"
workdir="${WORKDIR:-$(mktemp -d)}"
mkdir -p "$workdir"
dump_file="$workdir/ppts-drill.dump"
report_file="$workdir/ppts-drill-report.md"

backup_started=$(date +%s)
pg_dump --format=custom --no-owner --no-acl --file "$dump_file" "$SOURCE_DATABASE_URL"
backup_finished=$(date +%s)

restore_started=$(date +%s)
pg_restore --no-owner --no-acl --dbname "$RESTORE_DATABASE_URL" "$dump_file"
restore_finished=$(date +%s)

table_counts=$(psql "$RESTORE_DATABASE_URL" -v ON_ERROR_STOP=1 -At <<'SQL'
SELECT 'tenants=' || count(*) FROM tenants;
SELECT 'projects=' || count(*) FROM projects;
SELECT 'jobs=' || count(*) FROM jobs;
SELECT 'audit_events=' || count(*) FROM audit_events;
SELECT 'object_inventory=' || count(*) FROM object_inventory;
SQL
)

missing_objects=0
if [[ -n "${OBJECT_ROOT:-}" ]]; then
  while IFS= read -r key; do
    [[ -z "$key" ]] && continue
    if [[ ! -f "$OBJECT_ROOT/$key" ]]; then
      missing_objects=$((missing_objects + 1))
      printf 'missing object: %s\n' "$key" >&2
    fi
  done < <(psql "$RESTORE_DATABASE_URL" -v ON_ERROR_STOP=1 -At -c "SELECT object_key FROM object_inventory WHERE object_key <> ''")
fi

backup_seconds=$((backup_finished - backup_started))
restore_seconds=$((restore_finished - restore_started))
rpo_status="PASS"
rto_status="PASS"
object_status="SKIP"
if (( backup_interval > 900 )); then rpo_status="FAIL"; fi
if (( restore_seconds > 7200 )); then rto_status="FAIL"; fi
if [[ -n "${OBJECT_ROOT:-}" ]]; then
  object_status="PASS"
  if (( missing_objects > 0 )); then object_status="FAIL"; fi
fi

cat > "$report_file" <<EOF
# PPTS Backup/Restore Drill Report

- generated_at: $(date -Iseconds)
- dump_file: $dump_file
- backup_seconds: $backup_seconds
- restore_seconds: $restore_seconds
- configured_backup_interval_seconds: $backup_interval
- rpo_target_15m: $rpo_status
- rto_target_2h: $rto_status
- object_reference_check: $object_status
- missing_objects: $missing_objects

## Restored Table Counts

\`\`\`
$table_counts
\`\`\`
EOF

printf '%s\n' "$report_file"
if [[ "$rpo_status" != "PASS" || "$rto_status" != "PASS" || "$object_status" == "FAIL" ]]; then
  exit 1
fi
