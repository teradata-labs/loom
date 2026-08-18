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
// This file implements the Multi Round-Trip Requests (MRTR) pattern types of
// the 2026-07-28 revision (SEP-2322): servers embed their requests for
// additional input inside an input_required result, and the client retries
// the original request carrying the answers.
package protocol

import (
	"encoding/json"
	"fmt"
)

// InputRequest is one server-initiated request embedded in an
// InputRequiredResult: an ElicitRequest, CreateMessageRequest, or
// ListRootsRequest expressed as method + params.
type InputRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// InputRequests maps server-assigned identifiers (unique within one request)
// to the requests the client must fulfill before retrying.
type InputRequests map[string]InputRequest

// InputResponses maps the identifiers from InputRequests to the client's
// result object for each (e.g. an ElicitResult).
type InputResponses map[string]json.RawMessage

// InputRequiredResult is the interim result a server returns when it needs
// caller input before completing (resultType "input_required"). The
// specification requires at least one of InputRequests or RequestState to be
// present.
type InputRequiredResult struct {
	ResultType    string        `json:"resultType"`
	InputRequests InputRequests `json:"inputRequests,omitempty"`
	// RequestState is the server's opaque state blob. Presence is tracked
	// independently of value: the schema permits an empty string, which the
	// client must echo back exactly, while an absent field must stay absent
	// on the retry.
	RequestState *string `json:"requestState,omitempty"`
}

// ParseInputRequired decodes an input_required interim result.
func ParseInputRequired(result json.RawMessage) (*InputRequiredResult, error) {
	var irr InputRequiredResult
	if err := json.Unmarshal(result, &irr); err != nil {
		return nil, fmt.Errorf("failed to parse input_required result: %w", err)
	}
	if irr.ResultType != ResultTypeInputRequired {
		return nil, fmt.Errorf("result is not input_required (resultType %q)", irr.ResultType)
	}
	if len(irr.InputRequests) == 0 && irr.RequestState == nil {
		return nil, fmt.Errorf("input_required result carries neither inputRequests nor requestState")
	}
	return &irr, nil
}

// AttachRetryInput builds the params for an MRTR retry: the original params
// plus inputResponses and, when the server supplied one, the exact
// requestState echoed back — including a present-but-empty state, which the
// specification says must be echoed verbatim. It is always applied to the
// original params, so each round carries only the latest round's state, as
// the specification requires. requestState is omitted entirely when the
// server sent none (nil).
func AttachRetryInput(originalParams json.RawMessage, responses InputResponses, requestState *string) (json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	if len(originalParams) > 0 && string(originalParams) != "null" {
		if err := json.Unmarshal(originalParams, &obj); err != nil {
			return nil, fmt.Errorf("original params must be a JSON object: %w", err)
		}
	}
	if len(responses) > 0 {
		respJSON, err := json.Marshal(responses)
		if err != nil {
			return nil, err
		}
		obj["inputResponses"] = respJSON
	}
	if requestState != nil {
		stateJSON, err := json.Marshal(*requestState)
		if err != nil {
			return nil, err
		}
		obj["requestState"] = stateJSON
	} else {
		delete(obj, "requestState")
	}
	return json.Marshal(obj)
}
