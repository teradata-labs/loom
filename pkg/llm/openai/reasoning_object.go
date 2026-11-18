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

package openai

import (
	"context"
	"strings"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

// The Vercel AI Gateway's reasoning shape on Chat Completions, used when
// Config.ReasoningObject is on (docs, 2026-11):
//
//	request:  "reasoning": {"enabled": true, "effort": "high"}
//	response: message.reasoning (text) + message.reasoning_details[]
//
// reasoning_details entries may carry an upstream signature and are kept as
// llmtypes.ThinkingBlocks of type "reasoning.*", replayed on in-turn
// assistant messages the way litellm thinking_blocks are. It is not the
// litellm shape (thinking / thinking_blocks / reasoning_content), which stays
// byte-identical when ReasoningObject is off.

const (
	reasoningDetailPrefix    = "reasoning."
	reasoningDetailText      = "reasoning.text"
	reasoningDetailSummary   = "reasoning.summary"
	reasoningDetailEncrypted = "reasoning.encrypted"
)

// isReasoningDetail reports whether a thinking block came from
// reasoning_details rather than Anthropic/litellm thinking.
func isReasoningDetail(blockType string) bool {
	return strings.HasPrefix(blockType, reasoningDetailPrefix)
}

// detailToBlock keeps a reasoning_details entry as a ThinkingBlock. The
// payload field depends on the type; an unknown type keeps whichever payload
// it carried, so replay is lossless for the fields loom knows.
func detailToBlock(d WireReasoningDetail) llmtypes.ThinkingBlock {
	t := d.Type
	if t == "" {
		t = reasoningDetailText
	}
	payload := d.Text
	switch t {
	case reasoningDetailSummary:
		payload = d.Summary
	case reasoningDetailEncrypted:
		payload = d.Data
	default:
		if payload == "" {
			payload = firstNonEmpty(d.Summary, d.Data)
		}
	}
	return llmtypes.ThinkingBlock{Type: t, Thinking: payload, Signature: d.Signature, Format: d.Format}
}

// blockToDetail is the inverse of detailToBlock for replay.
func blockToDetail(b llmtypes.ThinkingBlock, index int) WireReasoningDetail {
	d := WireReasoningDetail{Type: b.Type, Signature: b.Signature, Format: b.Format}
	switch b.Type {
	case reasoningDetailSummary:
		d.Summary = b.Thinking
	case reasoningDetailEncrypted:
		d.Data = b.Thinking
	default:
		d.Text = b.Thinking
	}
	i := index
	d.Index = &i
	return d
}

// displayText is a detail's human-readable text: the reasoning or summary,
// never an encrypted payload.
func displayText(b llmtypes.ThinkingBlock) string {
	if b.Type == reasoningDetailEncrypted {
		return ""
	}
	return b.Thinking
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// modelReasons reports whether the model takes the reasoning object: a
// Claude-family id, or one the catalog marks is_reasoning (the gateway
// catalog's "reasoning" tag). An id the catalog does not know gets nothing,
// so a request to a model that cannot reason is never rejected for it.
func (c *Client) modelReasons() bool {
	if _, _, claude := anthropicFallbackPricing(c.model); claude {
		return true
	}
	info := catalog.Lookup(c.catalogProvider, c.model)
	return info != nil && info.GetIsReasoning()
}

// reasoningEffort maps a loom thinking level to the gateway's effort scale
// (low | medium | high). "auto" sets no effort, leaving it to the upstream.
func reasoningEffort(level string) string {
	switch strings.ToLower(level) {
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high", "xhigh", "max":
		return "high"
	}
	return ""
}

// reasoningParam returns the request's reasoning object, or nil when the
// option is off, thinking is off, or the model does not reason. Claude ids
// whose thinking is suppressed for this request (a rebuilt tool_use row that
// cannot carry the enabled form's required block) get none, as on the
// litellm path.
func (c *Client) reasoningParam(ctx context.Context) map[string]interface{} {
	if !c.reasoningObject || c.thinkingLevel == "" || c.thinkingLevel == "none" || !c.modelReasons() {
		return nil
	}
	if c.emitsCacheControl() && !llm.IsAdaptiveThinkingModel(c.model) && llm.ThinkingSuppressed(ctx) {
		return nil
	}
	r := map[string]interface{}{"enabled": true}
	if e := reasoningEffort(c.thinkingLevel); e != "" {
		r["effort"] = e
	}
	return r
}

// detailAccumulator merges streamed reasoning_details fragments by index.
type detailAccumulator struct {
	blocks map[int]*llmtypes.ThinkingBlock
	order  []int
}

func (a *detailAccumulator) add(d WireReasoningDetail) {
	idx := 0
	if d.Index != nil {
		idx = *d.Index
	}
	if a.blocks == nil {
		a.blocks = make(map[int]*llmtypes.ThinkingBlock)
	}
	frag := detailToBlock(d)
	blk, ok := a.blocks[idx]
	if !ok {
		blk = &llmtypes.ThinkingBlock{Type: frag.Type}
		a.blocks[idx] = blk
		a.order = append(a.order, idx)
	}
	if d.Type != "" {
		blk.Type = frag.Type
	}
	blk.Thinking += frag.Thinking
	if frag.Signature != "" {
		blk.Signature = frag.Signature
	}
	if frag.Format != "" {
		blk.Format = frag.Format
	}
}

func (a *detailAccumulator) result() []llmtypes.ThinkingBlock {
	out := make([]llmtypes.ThinkingBlock, 0, len(a.order))
	for _, idx := range a.order {
		out = append(out, *a.blocks[idx])
	}
	return out
}
