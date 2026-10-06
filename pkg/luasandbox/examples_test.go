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

package luasandbox

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The scripts below are the worked examples from the design document
// (docs/architecture/lua-script-tool.md, "Examples"). Keeping them as tests
// keeps the documentation honest.

func TestExampleBatchAndJoin(t *testing.T) {
	h := newFakeHost().with("execute_query", func(_ context.Context, args map[string]any) (*CallResult, error) {
		stmts := args["statements"].([]any)
		sql := stmts[0].(map[string]any)["sql"].(string)
		if strings.Contains(sql, "FROM sales") {
			return &CallResult{OK: true, Data: map[string]any{"rows": []any{
				map[string]any{"store_id": 1, "total": 10}, map[string]any{"store_id": 2, "total": 30},
			}}}, nil
		}
		return &CallResult{OK: true, Data: map[string]any{"rows": []any{
			map[string]any{"store_id": 1, "name": "North"}, map[string]any{"store_id": 2, "name": "South"},
		}}}, nil
	})
	res := Run(context.Background(), Program{Source: `
		local sales = tools.must("execute_query", {
		  statements = {{label = "sales", sql = "SELECT store_id, SUM(amount) AS total FROM sales WHERE region = '" .. args.region .. "' GROUP BY store_id"}}
		})
		local stores = tools.must("execute_query", {
		  statements = {{label = "stores", sql = "SELECT store_id, name FROM stores"}}
		})
		local nameById = {}
		for _, row in ipairs(stores.rows or {}) do nameById[row.store_id] = row.name end
		local out = {}
		for _, row in ipairs(sales.rows or {}) do
		  out[#out + 1] = {store = nameById[row.store_id] or row.store_id, total = row.total}
		end
		table.sort(out, function(a, b) return a.total > b.total end)
		return out`, Args: map[string]any{"region": "EU"}}, small(), h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, []any{
		map[string]any{"store": "South", "total": int64(30)},
		map[string]any{"store": "North", "total": int64(10)},
	}, res.Value)
	assert.Len(t, res.Calls, 2)
}

func TestExampleTryThenFallBack(t *testing.T) {
	h := newFakeHost().
		with("catalog_lookup", failing("NOT_FOUND", "no catalog entry")).
		with("execute_query", text(`[{"ColumnName":"id"}]`))
	res := Run(context.Background(), Program{Source: `
		local ok, data = pcall(tools.must, "catalog_lookup", {table = args.table})
		if not ok then
		  log("catalog_lookup failed, falling back to DBC:", data)
		  data = tools.must("execute_query", {statements = {{sql = "SELECT * FROM DBC.ColumnsV WHERE TableName = '" .. args.table .. "'"}}})
		end
		return data`, Args: map[string]any{"table": "orders"}}, small(), h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, []any{map[string]any{"ColumnName": "id"}}, res.Value)
	assert.Contains(t, res.Output, "catalog_lookup failed")
}

func TestExamplePollALongJob(t *testing.T) {
	polls := 0
	h := newFakeHost().
		with("start_gdp_job", text(`{"id":"job-1"}`)).
		with("get_gdp_job_status", func(context.Context, map[string]any) (*CallResult, error) {
			polls++
			if polls < 3 {
				return &CallResult{OK: true, Data: map[string]any{"state": "running"}}, nil
			}
			return &CallResult{OK: true, Data: map[string]any{"state": "done", "rows": 5}}, nil
		})
	res := Run(context.Background(), Program{Source: `
		local start = tools.must("start_gdp_job", {job = args.job})
		for i = 1, 20 do
		  local r = tools.call("get_gdp_job_status", {id = start.id})
		  if r.ok and r.data.state == "done" then return r.data end
		  if r.ok and r.data.state == "failed" then error("job failed: " .. tostring(r.data.reason)) end
		  time.sleep(5)
		end
		return {state = "still_running", id = start.id}`, Args: map[string]any{"job": "gdp"}}, small(), h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, map[string]any{"state": "done", "rows": int64(5)}, res.Value)
}

func TestExamplePostProcessTurnResult(t *testing.T) {
	h := newFakeHost()
	h.turn[42] = &CallResult{OK: true, Data: `{"columns":["status"],"rows":[{"status":"open"},{"status":"open"},{"status":"closed"}]}`}
	res := Run(context.Background(), Program{Source: `
		local r = turn.result(args.message_id)
		if not r.ok then error(r.error.message) end
		local counts = {}
		for _, row in ipairs(r.data.rows or r.data) do
		  local k = tostring(row[args.column])
		  counts[k] = (counts[k] or 0) + 1
		end
		local out = {}
		for k, v in pairs(counts) do out[#out + 1] = {value = k, count = v} end
		table.sort(out, function(a, b) return a.value < b.value end)
		return out`, Args: map[string]any{"message_id": 42, "column": "status"}}, small(), h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, []any{
		map[string]any{"value": "closed", "count": int64(1)},
		map[string]any{"value": "open", "count": int64(2)},
	}, res.Value)
}
