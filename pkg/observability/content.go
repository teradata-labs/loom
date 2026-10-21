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

import (
	"context"
	"sync/atomic"
)

// DefaultMaxContentLength is the number of runes TruncateContent keeps when
// no limit has been configured.
const DefaultMaxContentLength = 200

// truncationMarker is appended to content that TruncateContent shortened.
const truncationMarker = "…"

var maxContentLength atomic.Int64 //nolint:gochecknoglobals // process-wide setting, written once at startup

func init() { //nolint:gochecknoinits
	maxContentLength.Store(DefaultMaxContentLength)
}

// SetMaxContentLength sets the number of runes TruncateContent keeps in
// span content attributes (message, response and query previews). 0 keeps
// content whole; a negative value restores DefaultMaxContentLength.
// Servers call it once at startup from observability.otlp_max_content_length.
func SetMaxContentLength(n int) {
	if n < 0 {
		n = DefaultMaxContentLength
	}
	maxContentLength.Store(int64(n))
}

// MaxContentLength returns the current TruncateContent limit in runes;
// 0 means unlimited.
func MaxContentLength() int {
	return int(maxContentLength.Load())
}

type spanMetadataKey struct{}

// ContextWithSpanMetadata returns ctx carrying string tags (for example an
// experiment's condition and replication) that turn spans started under it
// record in their OpenInference metadata attribute. The map is copied.
func ContextWithSpanMetadata(ctx context.Context, md map[string]string) context.Context {
	c := make(map[string]string, len(md))
	for k, v := range md {
		c[k] = v
	}
	return context.WithValue(ctx, spanMetadataKey{}, c)
}

// SpanMetadataFromContext returns a copy of the tags ContextWithSpanMetadata
// stored in ctx, or nil.
func SpanMetadataFromContext(ctx context.Context) map[string]string {
	md, _ := ctx.Value(spanMetadataKey{}).(map[string]string)
	if md == nil {
		return nil
	}
	c := make(map[string]string, len(md))
	for k, v := range md {
		c[k] = v
	}
	return c
}

// TruncateContent bounds content recorded on spans. It keeps the first
// MaxContentLength() runes and appends "…" when it shortened s.
func TruncateContent(s string) string {
	limit := MaxContentLength()
	if limit == 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + truncationMarker
}
