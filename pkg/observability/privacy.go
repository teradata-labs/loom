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
package observability

import "regexp"

// PII redaction patterns (compiled once at package init).
// Shared by HawkTracer and OTelTracer.
var (
	emailPattern      = regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)
	phonePattern      = regexp.MustCompile(`\b\d{3}[-.]?\d{3}[-.]?\d{4}\b`)
	ssnPattern        = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)
	creditCardPattern = regexp.MustCompile(`\b\d{4}[-\s]?\d{4}[-\s]?\d{4}[-\s]?\d{4}\b`)
)
