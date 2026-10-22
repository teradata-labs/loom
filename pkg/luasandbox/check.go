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
	"context"
	"errors"
	"fmt"
	"io"

	rt "github.com/teradata-labs/loom/third_party/golua/runtime"
)

// Check reports whether source would compile as a script under lim, without
// running any of it: the size limit, the nesting screen and the compiler are
// the same ones Run applies. Hosts call it before saving a script. The error,
// when there is one, is suitable for the model ("<name>:<line>: message").
//
// Like Run, the check is bounded by lim.Wall, lim.CPUTicks and
// lim.MemoryBytes and stops when ctx ends: the nesting screen checks its
// context every 256 tokens and the compiler polls an interrupt. A check that
// stops early returns an error wrapping context.Canceled or
// context.DeadlineExceeded, so callers can tell it from an invalid script.
func Check(ctx context.Context, name, source string, lim Limits) (err error) {
	lim = lim.Normalize()
	if name == "" {
		name = "inline"
	}
	if cause := ctx.Err(); cause != nil {
		return fmt.Errorf("script check cancelled before it started: %w", cause)
	}
	if len(source) > lim.MaxSourceBytes {
		return fmt.Errorf("the script is %d bytes; the limit is %d", len(source), lim.MaxSourceBytes)
	}
	checkCtx, cancel := context.WithTimeout(ctx, lim.Wall)
	defer cancel()
	stopped := func() error {
		if cause := ctx.Err(); cause != nil {
			return fmt.Errorf("script check cancelled: %w", cause)
		}
		return fmt.Errorf("checking the script exceeded the wall time limit of %v: %w", lim.Wall, context.DeadlineExceeded)
	}
	if err := checkSourceShape(checkCtx, name, source); err != nil {
		if checkCtx.Err() != nil && errors.Is(err, checkCtx.Err()) {
			return stopped()
		}
		return err
	}
	defer func() {
		if rec := recover(); rec != nil {
			err = errors.New("internal error in the script engine while compiling")
		}
	}()
	r := rt.New(io.Discard)
	defer r.Close(nil)
	// Compile inside a limited context with an interrupt bound to checkCtx,
	// as Run does, so the vendored compiler's polls stop it promptly.
	intr := rt.NewInterrupt()
	stopWatch := context.AfterFunc(checkCtx, func() { intr.Trigger("script check stopped") })
	defer stopWatch()
	_, cerr := r.MainThread().CallContext(rt.RuntimeContextDef{
		HardLimits: rt.RuntimeResources{Cpu: lim.CPUTicks, Memory: lim.MemoryBytes},
		Interrupt:  intr,
	}, func() error {
		_, err := r.CompileAndLoadLuaChunk(name, []byte(source), rt.TableValue(r.GlobalEnv()))
		return err
	})
	if checkCtx.Err() != nil {
		return stopped()
	}
	if cerr != nil {
		return errors.New(boundError(cleanError(cerr)))
	}
	return nil
}
