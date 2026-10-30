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

// DefaultMaxContentLength is the number of runes TruncateContent keeps.
const DefaultMaxContentLength = 200

// truncationMarker is appended to content that TruncateContent shortened.
const truncationMarker = "…"

// TruncateContent bounds content recorded on spans. It keeps the first
// DefaultMaxContentLength runes and appends "…" when it shortened s.
func TruncateContent(s string) string {
	runes := []rune(s)
	if len(runes) <= DefaultMaxContentLength {
		return s
	}
	return string(runes[:DefaultMaxContentLength]) + truncationMarker
}
