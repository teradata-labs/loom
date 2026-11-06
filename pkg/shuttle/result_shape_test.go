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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConventionVacuousResult(t *testing.T) {
	insertInput := map[string]interface{}{"sql": "INSERT INTO vt SELECT ..."}
	createInput := map[string]interface{}{"sql": "CREATE VOLATILE TABLE vt (a INTEGER)"}

	tests := []struct {
		name    string
		input   map[string]interface{}
		data    interface{}
		vacuous bool
		judged  bool
	}{
		{"insert that inserted nothing (the measured poison)",
			insertInput, `{"activity_count":0,"status":"success"}`, true, true},
		{"insert that did real work",
			insertInput, `{"activity_count":55316,"status":"success"}`, false, true},
		{"DDL ack with zero activity is normal",
			createInput, `{"activity_count":0,"status":"success"}`, false, true},
		{"zero activity without sql input abstains from the verb rule",
			map[string]interface{}{}, `{"activity_count":0}`, false, true},
		{"camelCase affected-rows convention",
			insertInput, `{"rowsAffected":0}`, true, true},
		{"empty rowset",
			nil, `{"columns":[{"name":"c"}],"row_count":0,"rows":[]}`, true, true},
		{"aggregate over empty set: all-null row",
			nil, `{"columns":[{"name":"total"}],"row_count":1,"rows":[[null]]}`, true, true},
		{"zero is a real answer, not vacuous",
			nil, `{"row_count":1,"rows":[[0]]}`, false, true},
		{"real rows",
			nil, `{"row_count":2,"rows":[[1,"a"],[2,"b"]]}`, false, true},
		{"records convention, empty",
			nil, `{"records":[]}`, true, true},
		{"prose result abstains",
			nil, `Query complete. No output.`, false, false},
		{"JSON without recognized keys abstains",
			nil, `{"status":"success","message":"done"}`, false, false},
		{"non-string data abstains",
			nil, 42, false, false},
		{"nil result abstains",
			nil, nil, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result *Result
			if tt.data != nil {
				result = &Result{Success: true, Data: tt.data}
			}
			vacuous, judged := ConventionVacuousResult(tt.input, result)
			assert.Equal(t, tt.judged, judged, "judged")
			assert.Equal(t, tt.vacuous, vacuous, "vacuous")
		})
	}
}
