-- 000021_human_request_kind_summary.down.sql
ALTER TABLE human_requests
    DROP COLUMN IF EXISTS kind,
    DROP COLUMN IF EXISTS summary;
