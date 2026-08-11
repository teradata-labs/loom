-- 000022_human_request_params.down.sql
ALTER TABLE human_requests
    DROP COLUMN IF EXISTS params_json,
    DROP COLUMN IF EXISTS params_truncated;
