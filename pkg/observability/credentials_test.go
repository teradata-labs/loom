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
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Secrets planted in the cases below. None may survive redaction.
const (
	plantedBearer = "abc123def456ghi789"
	plantedTavily = "tvly-planted0123456789abcdef"
	plantedPass   = "hunter2-planted"
	plantedCookie = "sid=planted42cookie"
)

func TestRedactCredentialString(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		secrets []string // must be gone
		keep    []string // must remain
	}{
		{
			name:    "tool arguments: Authorization header, URL kept",
			in:      `{"headers":{"Authorization":"Bearer ` + plantedBearer + `"},"method":"GET","url":"https://api.example.com/v1/items?q=x"}`,
			secrets: []string{plantedBearer},
			keep:    []string{`"url":"https://api.example.com/v1/items?q=x"`, `"method":"GET"`, `"Authorization":"[REDACTED]"`},
		},
		{
			name:    "nested password at depth",
			in:      `{"config":{"db":{"host":"db.local","password":"` + plantedPass + `","port":5432}}}`,
			secrets: []string{plantedPass},
			keep:    []string{`"host":"db.local"`, `"port":5432`},
		},
		{
			name:    "web_search api_key argument",
			in:      `{"api_key":"` + plantedTavily + `","provider":"tavily","query":"AI conferences December 2026"}`,
			secrets: []string{plantedTavily},
			keep:    []string{`"query":"AI conferences December 2026"`, `"provider":"tavily"`},
		},
		{
			name: "LLM output: tool calls rendered as name(json) lines",
			in: "I'll fetch it.\nhttp_request({\"headers\":{\"Authorization\":\"Bearer " + plantedBearer + "\",\"Accept\":\"application/json\"},\"url\":\"https://x.test/a\"})\n" +
				"web_search({\"api_key\":\"" + plantedTavily + "\",\"query\":\"q\"})",
			secrets: []string{plantedBearer, plantedTavily},
			keep:    []string{"I'll fetch it.", `"Accept":"application/json"`, `"url":"https://x.test/a"`, `"query":"q"`},
		},
		{
			name:    "http_request result: response headers as arrays",
			in:      `{"body":"ok","headers":{"Content-Type":["text/plain"],"Set-Cookie":["` + plantedCookie + `; Path=/"],"Www-Authenticate":["Bearer realm=x"]},"status_code":200}`,
			secrets: []string{plantedCookie},
			keep:    []string{`"Content-Type":["text/plain"]`, `"status_code":200`, `"body":"ok"`},
		},
		{
			name:    "tool results as text lines (follow-up LLM input), JSON not a whole document",
			in:      "tool http_request result: {\"headers\":{\"Set-Cookie\":[\"" + plantedCookie + "\"]},\"status_code\":200}\ntool file_read result: done",
			secrets: []string{plantedCookie},
			keep:    []string{"tool file_read result: done", `"status_code":200`},
		},
		{
			name:    "JSON escaped inside a string",
			in:      `{"result":"{\"password\":\"` + plantedPass + `\",\"user\":\"bob\"}"}`,
			secrets: []string{plantedPass},
			keep:    []string{"bob"},
		},
		{
			name:    "escaped JSON in text that is not a document",
			in:      `payload was "{\"api_key\":\"` + plantedTavily + `\"}" truncated`,
			secrets: []string{plantedTavily},
		},
		{
			name:    "truncated JSON that does not decode",
			in:      `{"password":"` + plantedPass + `","notes":"long text that was cut off here...`,
			secrets: []string{plantedPass},
		},
		{
			name:    "header lines in plain text",
			in:      "GET /v1 HTTP/1.1\nAuthorization: Bearer " + plantedBearer + "\nCookie: " + plantedCookie + "\nAccept: */*",
			secrets: []string{plantedBearer, plantedCookie},
			keep:    []string{"Accept: */*", "GET /v1 HTTP/1.1"},
		},
		{
			name:    "Bearer and Basic credentials in prose",
			in:      "use Bearer " + plantedBearer + " or Basic dXNlcjpwYXNzd29yZA== to call it",
			secrets: []string{plantedBearer, "dXNlcjpwYXNzd29yZA=="},
			keep:    []string{"to call it"},
		},
		{
			name: "well-known key formats and JWTs",
			in: "keys: sk-ant-api03-planted0123456789 AKIAIOSFODNN7EXAMPLE ghp_planted0123456789planted0123456789 " +
				"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.planted_signature_value",
			secrets: []string{"sk-ant-api03-planted0123456789", "AKIAIOSFODNN7EXAMPLE", "ghp_planted0123456789planted0123456789", "planted_signature_value"},
			keep:    []string{"keys:"},
		},
		{
			name:    "credentials named in prose (a model repeating a secret)",
			in:      "Done: status 200, cookie " + plantedCookie + " received; the password is hunter2x and the API key: " + plantedTavily,
			secrets: []string{plantedCookie, "hunter2x", plantedTavily},
			keep:    []string{"Done: status 200", "received"},
		},
		{
			name:    "credential URL parameters, other parameters kept",
			in:      "https://serpapi.com/search?engine=google&api_key=planted0123&q=loom",
			secrets: []string{"planted0123"},
			keep:    []string{"engine=google", "q=loom"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactCredentialString(tt.in)
			for _, s := range tt.secrets {
				if strings.Contains(got, s) {
					t.Errorf("secret %q survived: %s", s, got)
				}
			}
			for _, k := range tt.keep {
				if !strings.Contains(got, k) {
					t.Errorf("lost %q: %s", k, got)
				}
			}
			if !strings.Contains(got, RedactedValue) {
				t.Errorf("no %s marker: %s", RedactedValue, got)
			}
		})
	}
}

func TestRedactCredentialString_KeepsNonCredentials(t *testing.T) {
	for _, in := range []string{
		`{"max_tokens":4096,"total_tokens":120,"token_count":7,"input_tokens":5,"model":"claude"}`,
		"The agent has a basic understanding of bearer authentication and token limits.",
		"Used tokens 1500 of 4096; the password policy requires rotation and the secret sauce is patience.",
		"Input tokens: 1234567, output tokens: 2048.",
		"guess 3: RED BLUE GREEN YELLOW exact=2 color_only=1",
		"{\n  \"pretty\": true,\n  \"items\": [1, 2, 3]\n}",
		`https://example.com/search?q=loom&page=2`,
		"short",
	} {
		if got := redactCredentialString(in); got != in {
			t.Errorf("changed text without credentials:\n in: %s\nout: %s", in, got)
		}
	}
}

func TestRedactCredentialString_ReencodedDocumentKeepsNumbers(t *testing.T) {
	in := `{"id":12345678901234567890,"password":"x-planted-value","ratio":0.125}`
	got := redactCredentialString(in)
	if strings.Contains(got, "x-planted-value") {
		t.Fatalf("password survived: %s", got)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("not JSON any more: %v: %s", err, got)
	}
	if string(doc["id"]) != "12345678901234567890" || string(doc["ratio"]) != "0.125" {
		t.Errorf("numbers changed: id=%s ratio=%s", doc["id"], doc["ratio"])
	}
}

func TestIsCredentialField(t *testing.T) {
	for _, k := range []string{"password", "Authorization", "X-API-Key", "api_key", "apiKey", "Set-Cookie", "cookie",
		"client_secret", "access_token", "next_page_token", "token", "aws_secret_access_key", "auth", "privateKey_x",
		"db.password", "SECRET_VALUE"} {
		if !isCredentialField(k) {
			t.Errorf("%q should name a credential", k)
		}
	}
	for _, k := range []string{"max_tokens", "total_tokens", "token_count", "tokens", "key", "keyword", "author", "url", "headers", "model"} {
		if isCredentialField(k) {
			t.Errorf("%q should not name a credential", k)
		}
	}
}

func TestRedactOTelSpan_CredentialsInsideValues(t *testing.T) {
	args := `{"headers":{"Authorization":"Bearer ` + plantedBearer + `"},"url":"https://x.test"}`
	span := &Span{
		Name: "agent.tool_execution",
		Attributes: map[string]interface{}{
			"input.value":  args,
			"output.value": "error: MISSING_API_KEY: key " + plantedTavily + " rejected",
			"tool.args":    map[string]interface{}{"config": map[string]interface{}{"password": plantedPass}},
			"tool.list":    []string{"ok", "token=" + plantedBearer + "&x=1 Bearer " + plantedBearer},
			"tool.name":    "http_request",
		},
		Events: []Event{{Name: "tool.result", Attributes: map[string]interface{}{"body": "Set-Cookie: " + plantedCookie}}},
		Status: Status{Code: StatusError, Message: "401 for Authorization: Bearer " + plantedBearer},
	}
	before := fmt.Sprintf("%v", span.Attributes)

	for _, cfg := range []PrivacyConfig{{RedactCredentials: true}, {RedactCredentials: true, RedactPII: true}} {
		out := RedactSpan(span, cfg)
		dump := fmt.Sprintf("%v %v %v", out.Attributes, out.Events, out.Status)
		for _, s := range []string{plantedBearer, plantedTavily, plantedPass, plantedCookie} {
			if strings.Contains(dump, s) {
				t.Errorf("cfg %+v: secret %q exported: %s", cfg, s, dump)
			}
		}
		if out.Attributes["tool.name"] != "http_request" {
			t.Errorf("cfg %+v: tool.name = %v", cfg, out.Attributes["tool.name"])
		}
		if !strings.Contains(fmt.Sprint(out.Attributes["input.value"]), `"url":"https://x.test"`) {
			t.Errorf("cfg %+v: url lost: %v", cfg, out.Attributes["input.value"])
		}
	}
	if fmt.Sprintf("%v", span.Attributes) != before || !strings.Contains(span.Status.Message, plantedBearer) {
		t.Error("the caller's span was modified")
	}
	if out := RedactSpan(span, PrivacyConfig{}); out.Attributes["input.value"] != args {
		t.Error("nothing may change when redaction is off")
	}
	if RedactSpan(nil, PrivacyConfig{RedactCredentials: true}) != nil {
		t.Error("RedactSpan(nil) must be nil")
	}
}
