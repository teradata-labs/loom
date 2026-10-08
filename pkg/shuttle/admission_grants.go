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

// ApprovalParamsMaxBytes bounds the JSON-encoded parameters an approval
// request carries to the person deciding it; longer parameter maps are cut.
// A hook that answers Ask for a call whose parameters exceed it asks a person
// to approve text they cannot fully see, so such a hook should Deny instead.
const ApprovalParamsMaxBytes = paramsMaxBytes

type admissionGrantsKey struct{}

// ContextWithAdmissionGrants returns ctx carrying grants as the admission
// grants of the call about to run. The executor installs them on the context a
// tool body receives (see Decision.Grant); hosts and tests that call a tool
// body directly may install them too. A nil or empty grants clears any grants
// ctx already carried. The slice is copied.
func ContextWithAdmissionGrants(ctx context.Context, grants []any) context.Context {
	var stored []any
	if len(grants) > 0 {
		stored = append(make([]any, 0, len(grants)), grants...)
	}
	return context.WithValue(ctx, admissionGrantsKey{}, stored)
}

// AdmissionGrantsFromContext returns the Grant of every Ask a person approved
// for the call whose tool body is running, in hook order, or nil when the call
// was not approved (allowed outright, or no hook asked). The slice is a copy.
func AdmissionGrantsFromContext(ctx context.Context) []any {
	grants, _ := ctx.Value(admissionGrantsKey{}).([]any)
	if len(grants) == 0 {
		return nil
	}
	return append(make([]any, 0, len(grants)), grants...)
}

// toolBodyContext returns the context a tool body runs with, after its call
// was admitted. It carries res.Grants when the call was approved with grants
// and none otherwise, even when ctx already held grants. It never carries an
// AskGrant: the grant has been spent on this call's admission, and a tool that
// calls another tool through the executor must not lift that nested call's Ask
// with an approval a person gave for something else.
func toolBodyContext(ctx context.Context, res AdmissionResult) context.Context {
	if AskGrantFromContext(ctx) != nil {
		ctx = ContextWithAskGrant(ctx, nil)
	}
	if res.Approved && len(res.Grants) > 0 {
		return ContextWithAdmissionGrants(ctx, res.Grants)
	}
	if ctx.Value(admissionGrantsKey{}) != nil {
		return ContextWithAdmissionGrants(ctx, nil)
	}
	return ctx
}
