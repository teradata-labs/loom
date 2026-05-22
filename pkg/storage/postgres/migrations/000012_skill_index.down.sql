-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000012_skill_index.down.sql

DROP INDEX IF EXISTS idx_skill_index_nodes_depth;
DROP INDEX IF EXISTS idx_skill_index_nodes_hash;
DROP INDEX IF EXISTS idx_skill_index_nodes_root;
DROP TABLE IF EXISTS skill_index_nodes;

DROP INDEX IF EXISTS idx_skill_indices_root;
DROP TABLE IF EXISTS skill_indices;
