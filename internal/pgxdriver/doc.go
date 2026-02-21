// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package pgxdriver provides a PostgreSQL connection pool backed by pgx/v5.
//
// Unlike the sqlitedriver package (which registers a database/sql driver),
// pgxdriver exposes a pgxpool.Pool directly for better performance and
// PostgreSQL-specific features like LISTEN/NOTIFY and COPY.
//
// Usage:
//
//	pool, err := pgxdriver.NewPool(ctx, cfg, tracer)
//	defer pool.Close()
package pgxdriver
