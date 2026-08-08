-- 000022_human_request_params.up.sql
-- Carry the held call's full parameters with a stored human request (CT-1P).
-- params_json holds the JSON-encoded parameter map, bounded at 8192 bytes by
-- the writer; params_truncated reports that the bound cut whole pairs out of
-- it. params_json is nullable with no default, so a pre-existing row reads back
-- NULL and consumers see an empty parameter map; params_truncated's constant
-- default makes a pre-existing row read false. user_id RLS is inherited
-- unchanged.
ALTER TABLE human_requests
    ADD COLUMN IF NOT EXISTS params_json TEXT,
    ADD COLUMN IF NOT EXISTS params_truncated BOOLEAN DEFAULT false;
