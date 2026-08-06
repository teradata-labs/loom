-- Context-compilation columns (HLD §4.5). seq is the existing messages.id
-- (BIGSERIAL); evicted/folded are the write-once relief flags; turn is derived
-- at insert. Legacy rows keep turn = 0 (uniformly old, which is correct).
ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS evicted BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS folded  BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS turn    BIGINT  NOT NULL DEFAULT 0;
