-- Repair queue metrics execute privileges for app/worker connections.
-- Some deployed databases predate the ppts_app grant in 0017 or lost it during
-- role recreation; keep the grant idempotent so worker backlog polling does not
-- spam permission-denied logs.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'ppts_queue_backlog_seconds') THEN
        GRANT EXECUTE ON FUNCTION ppts_queue_backlog_seconds() TO ppts_scheduler;
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ppts_app') THEN
            GRANT EXECUTE ON FUNCTION ppts_queue_backlog_seconds() TO ppts_app;
        END IF;
    END IF;
END $$;
