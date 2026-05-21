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
package tests

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestQuickstartScriptsOfferBedrockBearerToken(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Dir(filepath.Dir(filename))

	for _, path := range []string{"quickstart.sh", "quickstart.ps1"} {
		t.Run(path, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(repoRoot, path))
			if err != nil {
				t.Fatal(err)
			}
			script := string(content)

			if !strings.Contains(script, "AWS Bedrock (with Bearer Token)") {
				t.Fatalf("%s does not expose the Bedrock bearer token quickstart option", path)
			}
			if !strings.Contains(script, "bedrock_bearer_token") {
				t.Fatalf("%s does not configure the bedrock_bearer_token keyring entry", path)
			}
		})
	}
}
