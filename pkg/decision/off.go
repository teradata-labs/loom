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

package decision

import (
	"context"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Off is the decider for `provider: off`. It answers nothing and returns
// ErrDisabled, which a Router reports as DISABLED so every call site runs its
// existing mechanism. It exists so switching the layer off is a config value,
// not a nil check at every site.
type Off struct{}

// Decide always returns ErrDisabled.
func (Off) Decide(context.Context, *loomv1.DecisionRequest) (*loomv1.DecisionResponse, error) {
	return nil, ErrDisabled
}

// Name implements Decider.
func (Off) Name() string { return "off" }

// Model implements Decider.
func (Off) Model() string { return "" }
