-- Migration 009: Add name column to sessions table
-- Persists the human-readable session name from CreateSessionRequest.name
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS name TEXT;
