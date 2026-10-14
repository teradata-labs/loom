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
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestCappedWriter(t *testing.T) {
	w := newCappedWriter(30)
	_, _ = w.Write([]byte("short"))
	s, cut := w.String()
	assert.Equal(t, "short", s)
	assert.False(t, cut)

	w = newCappedWriter(30)
	for range 100 {
		_, _ = w.Write([]byte("0123456789"))
	}
	s, cut = w.String()
	assert.True(t, cut)
	assert.True(t, strings.HasPrefix(s, "0123456789"))
	assert.True(t, strings.HasSuffix(s, "0123456789"))
	assert.Contains(t, s, "bytes of output elided")
	assert.LessOrEqual(t, len(w.tail), 2*w.tailCap, "memory stays bounded")

	w = newCappedWriter(30)
	_, _ = w.Write([]byte(strings.Repeat("y", 1000)))
	s, cut = w.String()
	assert.True(t, cut)
	assert.Contains(t, s, "970 bytes") // 1000 written, 10 head + 20 tail kept

	w = newCappedWriter(1)
	_, _ = w.Write([]byte("abcdef"))
	_, cut = w.String()
	assert.True(t, cut)
}

func TestCappedWriterExactFit(t *testing.T) {
	w := newCappedWriter(30)
	_, _ = w.Write([]byte(strings.Repeat("a", 30)))
	s, cut := w.String()
	assert.False(t, cut)
	assert.Len(t, s, 30)
}

func TestOutputIsRuneSafe(t *testing.T) {
	w := newCappedWriter(40)
	for range 50 {
		_, _ = w.Write([]byte("日本語テキスト"))
	}
	s, _ := w.String()
	assert.True(t, utf8.ValidString(s), "compaction never splits a rune: %q", s)

	cut := truncateText(strings.Repeat("é", 1000), 200)
	assert.True(t, utf8.ValidString(cut))
	assert.LessOrEqual(t, len(cut), 200)
	assert.Contains(t, cut, "bytes elided")
	assert.Equal(t, "abc", truncateText("abc", 10))
	assert.True(t, utf8.ValidString(truncateText(strings.Repeat("é", 100), 5)))
}
