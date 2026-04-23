-- 000004_memory_event_date.up.sql
-- Add structured event-date fields to graph_memories so temporal facts
-- (e.g., "started watching The Crown about two months ago") can be
-- stored as an absolute ISO date rather than relying on prose inside
-- the content field. Ordering questions across multiple memories then
-- become lexicographic on event_date instead of requiring the
-- answer-time LLM to resolve independent relative phrases.
--
-- Both columns are nullable: memories without a time dimension leave
-- them NULL. event_date_confidence tracks how the extractor resolved
-- the date ("exact", "approximate", "ambiguous"); "ambiguous" means
-- the extractor saw a time cue but could not compute an absolute date
-- (e.g., "a while back") and deliberately chose not to fabricate one.

ALTER TABLE graph_memories ADD COLUMN event_date TEXT;
ALTER TABLE graph_memories ADD COLUMN event_date_confidence TEXT;
CREATE INDEX IF NOT EXISTS idx_memories_event_date ON graph_memories(event_date)
  WHERE event_date IS NOT NULL;
