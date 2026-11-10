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

//go:build linux

package shelljail

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDataLimit(t *testing.T) {
	base := func(v uint64, ok bool) func() (uint64, bool) {
		return func() (uint64, bool) { return v, ok }
	}
	for _, tt := range []struct {
		name   string
		memory uint64
		race   bool
		vmData func() (uint64, bool)
		want   uint64
	}{
		{name: "normal build keeps the limit", memory: 512 << 20, race: false, vmData: base(40<<20, true), want: 512 << 20},
		{name: "race build counts from the starting mappings", memory: 512 << 20, race: true, vmData: base(1200<<20, true), want: 1712 << 20},
		{name: "race build without /proc keeps the limit", memory: 512 << 20, race: true, vmData: base(0, false), want: 512 << 20},
		{name: "no limit stays no limit", memory: 0, race: true, vmData: base(1200<<20, true), want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, dataLimit(tt.memory, tt.race, tt.vmData))
		})
	}
}

func TestParseVmData(t *testing.T) {
	got, ok := parseVmData([]byte("Name:\tx\nVmSize:\t 2773792 kB\nVmData:\t 1191592 kB\nVmStk:\t 132 kB\n"))
	require.True(t, ok)
	assert.Equal(t, uint64(1191592)<<10, got)
	_, ok = parseVmData([]byte("VmSize:\t 1 kB\n"))
	assert.False(t, ok, "no VmData line")
	_, ok = parseVmData([]byte("VmData:\t x kB\n"))
	assert.False(t, ok, "unparsable value")
}

func TestCurrentVmData(t *testing.T) {
	v, ok := currentVmData()
	require.True(t, ok)
	assert.Positive(t, v)
}
