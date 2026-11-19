// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package openairesponses is an LLM provider client for the OpenAI Responses
// API (POST {base}/v1/responses). It implements types.StreamingLLMProvider
// (Name, Model, Chat, ChatStream) like pkg/llm/openai does for Chat
// Completions. The Vercel AI Gateway provider (pkg/llm/vercelgateway) is the
// first caller; it fronts any model id on this surface.
//
// # Request shape
//
// Every call sends the whole conversation. The client never sends
// previous_response_id (the gateway answers with gen_… generation ids, not
// provider response ids) and always sends "store": false. ExtraBody may not
// set previous_response_id either.
//
//   - Leading system messages are joined with a blank line into
//     "instructions". A system message after the first non-system message
//     stays in place as a {"role":"system"} input item.
//   - User text becomes an input_text part; user images become input_image
//     parts with a data: URL or the image URL, as pkg/llm/openai does.
//   - Assistant text becomes an output_text part. Each assistant tool call
//     becomes one function_call item keyed by call_id. Each tool result
//     becomes a function_call_output item; a result whose call_id matches no
//     assistant tool call in the request is dropped, because the API rejects
//     an orphan output.
//   - Function tools are flat ({"type":"function","name",...}) and carry
//     "strict": false. The Responses API makes function tools strict by
//     default, and loom tool schemas do not meet the strict-mode rules.
//   - Prompt-cache breakpoints (Message.CacheBreakpoint) are not sent: the
//     Responses API has no cache_control field.
//
// # Reasoning
//
// ThinkingLevel "low", "medium" or "high" sends
// "reasoning": {"effort", "summary": "auto"}; "auto" maps to "medium";
// "" and "none" send no reasoning object. With reasoning on, the request asks
// for include: ["reasoning.encrypted_content"] and omits temperature
// (reasoning models and Anthropic thinking accept only the default).
//
// A response's reasoning items are kept on LLMResponse.ThinkingBlocks, and
// therefore on the assistant types.Message the agent builds from it, as
// opaque blocks (see blockTypeReasoning). On the next request they are sent
// back as reasoning items, in their original order, before that assistant
// message's text and function_call items. With store=false, replaying
// encrypted_content is what lets a reasoning model keep its chain of thought
// across tool calls. The summary text goes to LLMResponse.Thinking.
//
// # Usage and cost
//
// The Responses API input_tokens count includes cached tokens (OpenAI
// convention), and the gateway's cache_write_tokens are treated the same way.
// Usage.InputTokens is input_tokens minus both cache buckets, so the loom
// buckets are disjoint, as in pkg/llm/openai (see pkg/types.Usage).
// Usage.RateLimitTokens is the raw total. CostUSD is the cost the
// ResponseInspector reports, else an estimate from the catalog.
//
// # Streaming
//
// ChatStream reads the SSE event stream. Text deltas go to the token
// callback in order. The final LLMResponse is built from the response object
// in response.completed or response.incomplete; the items from
// response.output_item.done are the fallback when that object carries no
// output. A stream that ends without a terminal event is an error.
package openairesponses
