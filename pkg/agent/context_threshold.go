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
package agent

import "github.com/teradata-labs/loom/pkg/storage"

// contextThreshold returns the one threshold value (HLD §5.1) in effect for
// this agent, in bytes.
func (a *Agent) contextThreshold() int {
	if a.sharedMemoryThreshold > 0 {
		if a.sharedMemoryThreshold < minThreshold {
			return minThreshold // §4.1 needs room for the tail; see minThreshold
		}
		return int(a.sharedMemoryThreshold)
	}
	return int(storage.DefaultSharedMemoryThreshold)
}
