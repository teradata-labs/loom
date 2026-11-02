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
package shuttle

import "context"

type workDirKey struct{}

// ContextWithWorkDir says where the work of the calls made under ctx is: a
// tool that takes a working directory starts there, and resolves relative
// paths there, in place of the directory it was built with. A tool is built
// once and serves many turns; where one turn works belongs to the turn.
func ContextWithWorkDir(ctx context.Context, dir string) context.Context {
	if dir == "" {
		return ctx
	}
	return context.WithValue(ctx, workDirKey{}, dir)
}

// WorkDirFromContext returns the working directory installed on ctx, or "".
func WorkDirFromContext(ctx context.Context) string {
	dir, _ := ctx.Value(workDirKey{}).(string)
	return dir
}
