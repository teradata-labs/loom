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
package version

// Version can be overridden at build time via ldflags:
// go build -ldflags="-X github.com/teradata-labs/loom/internal/version.Version=vX.Y.Z"
var Version = "1.0.2" // Default version

// Get returns the current version
func Get() string {
	if Version == "" {
		return "dev"
	}
	return Version
}
