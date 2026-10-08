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
	"errors"
	"fmt"
	"io"

	rt "github.com/teradata-labs/loom/third_party/golua/runtime"
)

// Check reports whether source would compile as a script under lim, without
// running any of it: the size limit, the nesting screen and the compiler are
// the same ones Run applies. Hosts call it before saving a script. The error,
// when there is one, is suitable for the model ("<name>:<line>: message").
func Check(name, source string, lim Limits) (err error) {
	lim = lim.Normalize()
	if name == "" {
		name = "inline"
	}
	if len(source) > lim.MaxSourceBytes {
		return fmt.Errorf("the script is %d bytes; the limit is %d", len(source), lim.MaxSourceBytes)
	}
	if err := checkSourceShape(name, source); err != nil {
		return err
	}
	defer func() {
		if rec := recover(); rec != nil {
			err = errors.New("internal error in the script engine while compiling")
		}
	}()
	r := rt.New(io.Discard)
	defer r.Close(nil)
	if _, err := r.CompileAndLoadLuaChunk(name, []byte(source), rt.TableValue(r.GlobalEnv())); err != nil {
		return errors.New(boundError(cleanError(err)))
	}
	return nil
}
