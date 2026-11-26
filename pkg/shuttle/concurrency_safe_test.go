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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// plainCSTool implements Tool only; it never declares ConcurrencySafe.
type plainCSTool struct{}

func (plainCSTool) Name() string        { return "plain" }
func (plainCSTool) Description() string { return "plain" }
func (plainCSTool) InputSchema() *JSONSchema {
	return NewObjectSchema("", nil, nil)
}
func (plainCSTool) Execute(context.Context, map[string]interface{}) (*Result, error) {
	return &Result{Success: true}, nil
}
func (plainCSTool) Backend() string { return "" }

// declaredCSTool adds the optional trait with a configurable answer.
type declaredCSTool struct {
	plainCSTool
	safe bool
}

func (d declaredCSTool) ConcurrencySafe() bool { return d.safe }

func TestIsConcurrencySafe_DefaultFalse(t *testing.T) {
	assert.False(t, IsConcurrencySafe(plainCSTool{}), "undeclared tool must fail closed")
	assert.False(t, IsConcurrencySafe(nil), "nil tool must fail closed")
}

func TestIsConcurrencySafe_TrueWhenDeclared(t *testing.T) {
	assert.True(t, IsConcurrencySafe(declaredCSTool{safe: true}))
}

func TestIsConcurrencySafe_FalseWhenDeclaredFalse(t *testing.T) {
	assert.False(t, IsConcurrencySafe(declaredCSTool{safe: false}))
}
