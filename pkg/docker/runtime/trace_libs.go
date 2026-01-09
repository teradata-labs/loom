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
package runtime

import (
	_ "embed"
)

// Embedded trace libraries for container-side tracing.
// These are automatically installed into containers to enable distributed tracing.

//go:embed python/loom_trace.py
var pythonTraceLibrary string

//go:embed node/loom-trace.js
var nodeTraceLibrary string

// GetPythonTraceLibrary returns the Python trace library source code.
func GetPythonTraceLibrary() string {
	return pythonTraceLibrary
}

// GetNodeTraceLibrary returns the Node.js trace library source code.
func GetNodeTraceLibrary() string {
	return nodeTraceLibrary
}
