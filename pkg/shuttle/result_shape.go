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
package shuttle

import (
	"encoding/json"
	"strings"
)

// VacuousResultJudge is an optional capability a Tool can implement to
// report whether one of its own successful results actually did work.
// Tool-level success is not task-level progress: a call that "fixes" an
// error by matching zero rows returns success while doing nothing, and any
// learning layer that trusts it amplifies confident empty answers.
//
// The judgment is deliberately tri-state: judged=false means this tool
// cannot tell for this result (the caller falls back to convention matching
// and, past that, to model judgment over a result preview). A tool should
// implement this only where it can be EXACT about its own result schema —
// that keeps endpoint knowledge with the endpoint's adapter instead of
// leaking one server's conventions into the framework.
type VacuousResultJudge interface {
	// VacuousSuccess inspects a successful result. vacuous is meaningful
	// only when judged is true.
	VacuousSuccess(input map[string]interface{}, result *Result) (vacuous, judged bool)
}

// rowActivityVerbs are the SQL verbs for which zero affected rows means the
// statement did nothing. DDL (CREATE/DROP/...) legitimately acks with zero
// activity and is never judged vacuous by an activity count.
var rowActivityVerbs = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true,
}

// normalizeShapeKey folds naming conventions together: row_count, rowCount
// and RowCount all become "rowcount".
func normalizeShapeKey(k string) string {
	return strings.ReplaceAll(strings.ToLower(k), "_", "")
}

// key families the convention matcher recognizes, post-normalization.
var (
	rowCountKeys = map[string]bool{"rowcount": true}
	activityKeys = map[string]bool{
		"activitycount": true, "rowsaffected": true, "affectedrows": true,
		"rowsinserted": true, "rowsupdated": true, "rowsdeleted": true,
	}
	rowArrayKeys = map[string]bool{"rows": true, "records": true}
)

// ConventionVacuousResult is the batteries-included middle layer of vacuous
// judgment: it recognizes structured result payloads by the row-count /
// affected-rows / row-array conventions shared across SQL-ish tools (in any
// of their snake_case / camelCase spellings) and ABSTAINS (judged=false) on
// everything it cannot positively identify. It encodes conventions, never a
// specific server's schema; a tool wanting exact judgment implements
// VacuousResultJudge and wins over this.
//
// Rules, applied to a successful result whose Data is a JSON object:
//   - a recognized row array that is empty, or whose every value is null
//     (aggregates over an empty set), is vacuous;
//   - a recognized row count of 0 is vacuous;
//   - a recognized affected-rows count of 0 is vacuous ONLY when the call's
//     "sql" input starts with a row-moving verb (INSERT/UPDATE/DELETE/MERGE);
//     DDL acks with 0 activity are normal.
func ConventionVacuousResult(input map[string]interface{}, result *Result) (vacuous, judged bool) {
	if result == nil {
		return false, false
	}
	data, ok := result.Data.(string)
	if !ok || data == "" {
		return false, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return false, false
	}

	recognized := false
	for k, v := range raw {
		switch nk := normalizeShapeKey(k); {
		case rowArrayKeys[nk]:
			var rows []json.RawMessage
			if err := json.Unmarshal(v, &rows); err != nil {
				continue
			}
			recognized = true
			if len(rows) == 0 || allRowValuesNull(rows) {
				return true, true
			}
		case rowCountKeys[nk]:
			var n float64
			if err := json.Unmarshal(v, &n); err != nil {
				continue
			}
			recognized = true
			if n == 0 {
				return true, true
			}
		case activityKeys[nk]:
			var n float64
			if err := json.Unmarshal(v, &n); err != nil {
				continue
			}
			recognized = true
			if n == 0 && sqlRowActivityExpected(input) {
				return true, true
			}
		}
	}
	return false, recognized
}

// allRowValuesNull reports whether every value in every row is JSON null —
// the shape aggregates take over an empty set (SUM over zero matched rows).
func allRowValuesNull(rows []json.RawMessage) bool {
	for _, row := range rows {
		var vals []interface{}
		if err := json.Unmarshal(row, &vals); err != nil || len(vals) == 0 {
			return false
		}
		for _, v := range vals {
			if v != nil {
				return false
			}
		}
	}
	return true
}

// sqlRowActivityExpected reports whether the call's SQL starts with a verb
// for which zero affected rows means nothing happened.
func sqlRowActivityExpected(input map[string]interface{}) bool {
	sqlRaw, ok := input["sql"].(string)
	if !ok {
		return false
	}
	fields := strings.Fields(strings.ToUpper(sqlRaw))
	return len(fields) > 0 && rowActivityVerbs[fields[0]]
}
