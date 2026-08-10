-- 000021_human_request_kind_summary.up.sql
-- Let a stored human request self-describe (CT-1). kind classifies the request
-- at origin ("approval" for a hook-held call, "question" for contact_human);
-- summary is the display digest (tool+args or the question text). Both nullable
-- with no default, so pre-existing rows read back NULL and consumers treat an
-- absent kind as "question" with an empty summary. user_id RLS is inherited
-- unchanged.
ALTER TABLE human_requests
    ADD COLUMN IF NOT EXISTS kind TEXT,
    ADD COLUMN IF NOT EXISTS summary TEXT;
