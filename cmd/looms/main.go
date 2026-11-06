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
package main

// gitCommit is the source revision this binary was built from. Image builds
// set it with -ldflags "-X main.gitCommit=<sha>" (deploy/longmemeval/
// Dockerfile); a plain go build leaves it "unknown".
var gitCommit = "unknown"

func main() {
	Execute()
}
