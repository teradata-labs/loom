-- One turn at a time per session: who is taking a turn in it now, since when
-- (unix ms, refreshed while the turn runs), and how many turns it has had.
-- '' and 0 are every existing row: free, so this changes no behaviour until a
-- turn is taken.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS turn_holder TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS turn_since BIGINT NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS turns BIGINT NOT NULL DEFAULT 0;
