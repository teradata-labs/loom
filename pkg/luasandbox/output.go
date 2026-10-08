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

package luasandbox

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// cappedWriter captures script output in bounded memory. It keeps the first
// third of the limit and a rolling window of the last two thirds, so both the
// start of the output and the final lines (usually the interesting ones)
// survive any volume of writes.
type cappedWriter struct {
	headCap int
	tailCap int
	head    []byte
	tail    []byte
	total   int64
}

func newCappedWriter(limit int) *cappedWriter {
	if limit < 3 {
		limit = 3
	}
	headCap := limit / 3
	return &cappedWriter{headCap: headCap, tailCap: limit - headCap}
}

// Write never fails and never retains more than twice the tail window.
func (w *cappedWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.total += int64(n)
	if room := w.headCap - len(w.head); room > 0 {
		take := min(room, len(p))
		w.head = append(w.head, p[:take]...)
		p = p[take:]
	}
	if len(p) == 0 {
		return n, nil
	}
	if len(p) >= w.tailCap {
		w.tail = append(w.tail[:0], p[len(p)-w.tailCap:]...)
		return n, nil
	}
	w.tail = append(w.tail, p...)
	if len(w.tail) > 2*w.tailCap {
		keep := w.tail[len(w.tail)-w.tailCap:]
		w.tail = append(w.tail[:0], keep...)
	}
	return n, nil
}

// warner adapts the writer to golua's Warner so runtime warnings (for example
// an error inside a finalizer) land in the captured output, not on stderr.
type warner struct{ w *cappedWriter }

func (wr warner) Warn(msgs ...string) {
	_, _ = wr.w.Write([]byte("warning: " + strings.Join(msgs, "") + "\n"))
}

// String renders the captured output and reports whether anything was elided.
func (w *cappedWriter) String() (string, bool) {
	kept := int64(len(w.head) + len(w.tail))
	if w.total <= int64(w.headCap+w.tailCap) && w.total == kept {
		return string(w.head) + string(w.tail), false
	}
	tail := w.tail
	if len(tail) > w.tailCap {
		tail = tail[len(tail)-w.tailCap:]
	}
	head := trimToRuneEnd(w.head)
	tail = trimToRuneStart(tail)
	elided := w.total - int64(len(head)) - int64(len(tail))
	return fmt.Sprintf("%s\n[... %d bytes of output elided ...]\n%s", head, elided, tail), true
}

// trimToRuneEnd drops a trailing partial UTF-8 sequence.
func trimToRuneEnd(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if utf8.FullRune(b[i:]) {
				return b
			}
			return b[:i]
		}
	}
	return b
}

// trimToRuneStart drops a leading partial UTF-8 sequence.
func trimToRuneStart(b []byte) []byte {
	for i := 0; i < len(b) && i < utf8.UTFMax; i++ {
		if utf8.RuneStart(b[i]) {
			return b[i:]
		}
	}
	return b
}

// truncateText cuts s to at most limit bytes on rune boundaries, keeping the
// first three quarters and the last quarter of the budget around a marker.
func truncateText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	marker := fmt.Sprintf("\n[... %d bytes elided: the result exceeded the per-call limit of %d bytes ...]\n", len(s), limit)
	budget := limit - len(marker)
	if budget < 8 {
		return string(trimToRuneEnd([]byte(s[:max(limit, 0)])))
	}
	headN := budget * 3 / 4
	tailN := budget - headN
	head := trimToRuneEnd([]byte(s[:headN]))
	tail := trimToRuneStart([]byte(s[len(s)-tailN:]))
	return string(head) + marker + string(tail)
}
