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

package pgxdriver

import (
	"testing"

	"github.com/stretchr/testify/assert"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestBuildSupabaseDSN_FromRegion(t *testing.T) {
	dsn := buildSupabaseDSN(&loomv1.SupabaseConfig{
		Enabled:          true,
		ProjectRef:       "abcdefghijklmnop",
		DatabasePassword: "s3cret",
		Region:           "us-east-1",
	})
	assert.Equal(t,
		"postgresql://postgres.abcdefghijklmnop:s3cret@aws-0-us-east-1.pooler.supabase.com:5432/postgres?sslmode=require",
		dsn)
}

func TestBuildSupabaseDSN_PoolerHostOverride(t *testing.T) {
	dsn := buildSupabaseDSN(&loomv1.SupabaseConfig{
		Enabled:          true,
		ProjectRef:       "ref1",
		DatabasePassword: "pw",
		Region:           "us-east-1", // ignored when pooler_host is set
		PoolerHost:       "aws-1-eu-west-2.pooler.supabase.com",
		Database:         "analytics",
	})
	assert.Equal(t,
		"postgresql://postgres.ref1:pw@aws-1-eu-west-2.pooler.supabase.com:5432/analytics?sslmode=require",
		dsn)
}

func TestBuildSupabaseDSN_PasswordIsURLEncoded(t *testing.T) {
	dsn := buildSupabaseDSN(&loomv1.SupabaseConfig{
		Enabled:          true,
		ProjectRef:       "ref",
		DatabasePassword: "p@ss/w:rd?#",
		Region:           "us-east-1",
	})
	assert.Contains(t, dsn, "postgres.ref:p%40ss%2Fw%3Ard%3F%23@")
	assert.NotContains(t, dsn, "p@ss/w:rd?#")
}

func TestBuildSupabaseDSN_AlwaysSessionMode(t *testing.T) {
	dsn := buildSupabaseDSN(&loomv1.SupabaseConfig{
		Enabled: true, ProjectRef: "r", DatabasePassword: "p", Region: "us-east-1",
	})
	assert.Contains(t, dsn, ":5432/", "storage must use session mode (5432), never 6543")
	assert.NotContains(t, dsn, ":6543/")
}

func TestBuildSupabaseDSN_MissingFieldsReturnEmpty(t *testing.T) {
	cases := map[string]*loomv1.SupabaseConfig{
		"no project ref":        {Enabled: true, DatabasePassword: "p", Region: "us-east-1"},
		"no password":           {Enabled: true, ProjectRef: "r", Region: "us-east-1"},
		"no region and no host": {Enabled: true, ProjectRef: "r", DatabasePassword: "p"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, buildSupabaseDSN(cfg))
		})
	}
}

func TestBuildDSN_SupabasePrecedence(t *testing.T) {
	supa := &loomv1.SupabaseConfig{
		Enabled: true, ProjectRef: "ref", DatabasePassword: "pw", Region: "us-east-1",
	}

	t.Run("explicit dsn wins over supabase", func(t *testing.T) {
		cfg := &loomv1.PostgresStorageConfig{
			Dsn:      "postgres://u:p@host:5432/db",
			Supabase: supa,
		}
		assert.Equal(t, "postgres://u:p@host:5432/db", buildDSN(cfg))
	})

	t.Run("supabase used when enabled and no dsn", func(t *testing.T) {
		cfg := &loomv1.PostgresStorageConfig{Supabase: supa}
		assert.Contains(t, buildDSN(cfg), "postgres.ref:pw@aws-0-us-east-1.pooler.supabase.com:5432")
	})

	t.Run("supabase ignored when disabled (falls through to host fields)", func(t *testing.T) {
		cfg := &loomv1.PostgresStorageConfig{
			Host:     "localhost",
			Database: "loomdb",
			Supabase: &loomv1.SupabaseConfig{Enabled: false, ProjectRef: "ref", DatabasePassword: "pw"},
		}
		dsn := buildDSN(cfg)
		assert.Contains(t, dsn, "host='localhost'")
		assert.NotContains(t, dsn, "supabase.com")
	})
}
