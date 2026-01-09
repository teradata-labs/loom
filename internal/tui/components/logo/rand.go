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
package logo

import (
	"math/rand/v2"
	"sync"
)

var (
	randCaches   = make(map[int]int)
	randCachesMu sync.Mutex
)

// cachedRandN returns a cached random number for UI animations.
// Uses math/rand as crypto/rand is not needed for visual effects.
func cachedRandN(n int) int {
	randCachesMu.Lock()
	defer randCachesMu.Unlock()

	if n, ok := randCaches[n]; ok {
		return n
	}

	// #nosec G404 -- UI animation caching, not security-sensitive
	r := rand.IntN(n)
	randCaches[n] = r
	return r
}
