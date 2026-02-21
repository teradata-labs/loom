-- 000001_initial_schema.down.sql
-- Reverse the initial schema creation

DROP TABLE IF EXISTS human_requests CASCADE;
DROP TABLE IF EXISTS sql_result_metadata CASCADE;
DROP TABLE IF EXISTS agent_errors CASCADE;
DROP TABLE IF EXISTS artifacts CASCADE;
DROP TABLE IF EXISTS memory_snapshots CASCADE;
DROP TABLE IF EXISTS tool_executions CASCADE;
DROP TABLE IF EXISTS messages CASCADE;
DROP TABLE IF EXISTS sessions CASCADE;
DROP TABLE IF EXISTS schema_migrations CASCADE;
