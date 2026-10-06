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

import "time"

// Limits bounds one run.
//
// A zero or negative field means "use the package default" (DefaultLimits) and
// a field above its ceiling (MaxLimits) is lowered to the ceiling, so a run is
// never unbounded whatever a host passes.
type Limits struct {
	// Wall bounds the whole run, including time spent inside tool calls and
	// sleeps.
	Wall time.Duration
	// CPUTicks bounds pure computation. One tick is one interpreter step;
	// about 2e8 ticks run per second on a modern core.
	CPUTicks uint64
	// MemoryBytes bounds total allocation by the script, including every
	// value a host function hands to it.
	MemoryBytes uint64
	// MaxToolCalls bounds tools.call, tools.must and turn.result together.
	MaxToolCalls int
	// ToolCallTimeout bounds one nested call. The run's own deadline still
	// applies when it is sooner.
	ToolCallTimeout time.Duration
	// MaxCallResultBytes bounds one nested call's result (larger results are
	// truncated before the script sees them) and one call's arguments.
	MaxCallResultBytes int
	// MaxOutputBytes bounds captured print/log output; the head and the tail
	// are kept when it overflows.
	MaxOutputBytes int
	// MaxResultBytes bounds the script's converted return value; larger values
	// are truncated.
	MaxResultBytes int
	// MaxSourceBytes bounds the script's source text.
	MaxSourceBytes int
	// MaxSleep bounds one time.sleep call.
	MaxSleep time.Duration
}

// DefaultLimits returns the limits used for any field a host leaves at zero.
func DefaultLimits() Limits {
	return Limits{
		Wall:     120 * time.Second,
		CPUTicks: 2_000_000_000,
		// golua undercharges small tables up to about 10x (see
		// PeakMemoryOverhead), so 64 MiB of budget can mean ~700 MB of
		// real memory for a hostile script.
		MemoryBytes:        64 << 20,
		MaxToolCalls:       100,
		ToolCallTimeout:    60 * time.Second,
		MaxCallResultBytes: 1 << 20,
		MaxOutputBytes:     16 << 10,
		MaxResultBytes:     256 << 10,
		MaxSourceBytes:     64 << 10,
		MaxSleep:           30 * time.Second,
	}
}

// MaxLimits returns the ceiling for every field. Normalize lowers any larger
// value to these, so an admin setting cannot exceed them either.
func MaxLimits() Limits {
	return Limits{
		Wall:               600 * time.Second,
		CPUTicks:           10_000_000_000,
		MemoryBytes:        256 << 20,
		MaxToolCalls:       1000,
		ToolCallTimeout:    300 * time.Second,
		MaxCallResultBytes: 8 << 20,
		MaxOutputBytes:     256 << 10,
		MaxResultBytes:     4 << 20,
		MaxSourceBytes:     1 << 20,
		MaxSleep:           120 * time.Second,
	}
}

type limitValue interface {
	~int | ~int64 | ~uint64
}

// pick returns def when v is unset and ceil when v exceeds it.
func pick[T limitValue](v, def, ceil T) T {
	if v <= 0 {
		return def
	}
	if v > ceil {
		return ceil
	}
	return v
}

// Normalize returns l with unset fields replaced by DefaultLimits and every
// field lowered to MaxLimits.
func (l Limits) Normalize() Limits {
	d, c := DefaultLimits(), MaxLimits()
	return Limits{
		Wall:               pick(l.Wall, d.Wall, c.Wall),
		CPUTicks:           pick(l.CPUTicks, d.CPUTicks, c.CPUTicks),
		MemoryBytes:        pick(l.MemoryBytes, d.MemoryBytes, c.MemoryBytes),
		MaxToolCalls:       pick(l.MaxToolCalls, d.MaxToolCalls, c.MaxToolCalls),
		ToolCallTimeout:    pick(l.ToolCallTimeout, d.ToolCallTimeout, c.ToolCallTimeout),
		MaxCallResultBytes: pick(l.MaxCallResultBytes, d.MaxCallResultBytes, c.MaxCallResultBytes),
		MaxOutputBytes:     pick(l.MaxOutputBytes, d.MaxOutputBytes, c.MaxOutputBytes),
		MaxResultBytes:     pick(l.MaxResultBytes, d.MaxResultBytes, c.MaxResultBytes),
		MaxSourceBytes:     pick(l.MaxSourceBytes, d.MaxSourceBytes, c.MaxSourceBytes),
		MaxSleep:           pick(l.MaxSleep, d.MaxSleep, c.MaxSleep),
	}
}

// tighten returns the smaller of base and req, treating an unset req as "no
// request".
func tighten[T limitValue](base, req T) T {
	if req > 0 && req < base {
		return req
	}
	return base
}

// Clamp returns the limits for one run when a caller asks for req (for
// example a per-call timeout). Each set field of req may only lower the
// corresponding field of l, never raise it. The result is normalized.
func (l Limits) Clamp(req Limits) Limits {
	l = l.Normalize()
	return Limits{
		Wall:               tighten(l.Wall, req.Wall),
		CPUTicks:           tighten(l.CPUTicks, req.CPUTicks),
		MemoryBytes:        tighten(l.MemoryBytes, req.MemoryBytes),
		MaxToolCalls:       tighten(l.MaxToolCalls, req.MaxToolCalls),
		ToolCallTimeout:    tighten(l.ToolCallTimeout, req.ToolCallTimeout),
		MaxCallResultBytes: tighten(l.MaxCallResultBytes, req.MaxCallResultBytes),
		MaxOutputBytes:     tighten(l.MaxOutputBytes, req.MaxOutputBytes),
		MaxResultBytes:     tighten(l.MaxResultBytes, req.MaxResultBytes),
		MaxSourceBytes:     tighten(l.MaxSourceBytes, req.MaxSourceBytes),
		MaxSleep:           tighten(l.MaxSleep, req.MaxSleep),
	}
}
