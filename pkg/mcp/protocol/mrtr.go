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

// InputRequiredError is returned by a server-side handler that needs caller
// input before completing. The server core converts it into an
// InputRequiredResult (resultType "input_required") for stateless clients;
// handler signatures stay unchanged. RequestState must survive a stateless
// retry that may land on another replica — seal it (internal/mcpstate) when
// it influences authorization or business logic.
type InputRequiredError struct {
	Requests     InputRequests
	RequestState string
}

func (e *InputRequiredError) Error() string {
	return fmt.Sprintf("caller input required (%d requests)", len(e.Requests))
}

// RequestStatePtr adapts the producer-side state to the wire field, which
// tracks presence: a pausing handler that supplies no state emits no
// requestState member at all (emitting a present-but-empty one would bind
// the client to echo a value that means nothing to this server).
func (e *InputRequiredError) RequestStatePtr() *string {
	if e.RequestState == "" {
		return nil
	}
	s := e.RequestState
	return &s
}

// RetryInput is the MRTR payload a client attaches when retrying the
// original request: its answers plus the echoed opaque state.
type RetryInput struct {
	Responses    InputResponses
	RequestState string
}

// ParseRetryInput extracts inputResponses and requestState from a retried
// request's params; both zero when this is not an MRTR retry. An absent
// member is not a retry and parses clean, but a present-and-malformed member
// is a client error the server must answer with InvalidParams — silently
// zeroing it would make the retry indistinguishable from an initial call and
// re-elicit the same input until MaxRounds.
func ParseRetryInput(params json.RawMessage) (RetryInput, error) {
	if len(params) == 0 {
		return RetryInput{}, nil
	}
	var raw struct {
		InputResponses json.RawMessage `json:"inputResponses"`
		RequestState   json.RawMessage `json:"requestState"`
	}
	if err := json.Unmarshal(params, &raw); err != nil {
		// Non-object params are the owning method's problem, not MRTR's.
		return RetryInput{}, nil
	}
	var out RetryInput
	if len(raw.InputResponses) > 0 && string(raw.InputResponses) != "null" {
		if err := json.Unmarshal(raw.InputResponses, &out.Responses); err != nil {
			return RetryInput{}, fmt.Errorf("malformed inputResponses: %w", err)
		}
	}
	if len(raw.RequestState) > 0 && string(raw.RequestState) != "null" {
		if err := json.Unmarshal(raw.RequestState, &out.RequestState); err != nil {
			return RetryInput{}, fmt.Errorf("malformed requestState: %w", err)
		}
	}
	return out, nil
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
