// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/teradata-labs/loom/pkg/eventlog"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

func unknownOutcomeAgent(t *testing.T, policy UnknownOutcomePolicy) (*Agent, *recordingEventLog, *crashingTool) {
	t.Helper()
	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithEventLog(recorder), WithUnknownOutcomePolicy(policy))
	tool := &crashingTool{}
	ag.tools.Register(tool)
	return ag, recorder, tool
}

// threeCallBatch builds the crash state: c1 ran and has a recorded result, c2 has an
// intent and no result (unknown outcome), c3 was never attempted. No tool rows.
func threeCallBatch(t *testing.T, ag *Agent, recorder *recordingEventLog, id string) *Session {
	t.Helper()
	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, id, ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "run crashy three times"}, true)
	ag.appendMessage(ctx, sess, Message{
		Role: "assistant",
		ToolCalls: []llmtypes.ToolCall{
			{ID: "c1", Name: "crashy", Input: map[string]interface{}{"n": 1}},
			{ID: "c2", Name: "crashy", Input: map[string]interface{}{"n": 2}},
			{ID: "c3", Name: "crashy", Input: map[string]interface{}{"n": 3}},
		},
	}, false)
	recordTailBatch(t, recorder, sess, 0)
	for _, c := range []string{"c1", "c2"} {
		pos := int(c[1] - '1')
		if err := recorder.RecordIntent(ctx, sess.ID, 0, pos, eventlog.EventToolCallIntent, c, []byte(`{"tool_name":"crashy"}`)); err != nil {
			t.Fatalf("fixture RecordIntent %s: %v", c, err)
		}
	}
	if err := recorder.RecordResult(ctx, sess.ID, 0, 0, eventlog.EventToolCallResult, "c1", []byte(`{"result":{"success":true,"data":"ok"},"error":""}`)); err != nil {
		t.Fatalf("fixture RecordResult: %v", err)
	}
	return sess
}

func toolMessage(sess *Session, callID string) *Message {
	for _, m := range rawSessionMessages(sess) {
		if m.Role == "tool" && m.ToolUseID == callID {
			m := m
			return &m
		}
	}
	return nil
}

func TestResumeSession_UnknownOutcomeFail_ClosesTheCallAndGoesOn(t *testing.T) {
	ag, recorder, tool := unknownOutcomeAgent(t, UnknownOutcomeFail)
	sess := threeCallBatch(t, ag, recorder, "s-unknown-fail")

	resp, err := ag.ResumeSession(context.Background(), "s-unknown-fail")
	if err != nil {
		t.Fatalf("ResumeSession = %v, want the turn to go on", err)
	}
	if resp == nil || resp.Content == "" {
		t.Fatalf("ResumeSession response = %+v, want the resumed turn's reply", resp)
	}
	if tool.calls != 1 {
		t.Errorf("crashingTool.calls = %d, want 1: only c3 (never attempted) runs; c1 is backfilled and c2 must not be re-run", tool.calls)
	}

	sess = ag.memory.GetOrCreateSessionWithAgent(context.Background(), "s-unknown-fail", ag.config.Name, "")
	m := toolMessage(sess, "c2")
	if m == nil || m.ToolResult == nil {
		t.Fatalf("no tool message for c2: %+v", m)
	}
	if m.ToolResult.Success || m.ToolResult.Error == nil || m.ToolResult.Error.Code != "outcome_unknown" {
		t.Errorf("c2 result = %+v, want a failed outcome_unknown result", m.ToolResult)
	}
	if !strings.Contains(m.ToolResult.Error.Message, "outcome unknown") {
		t.Errorf("c2 message %q does not say the outcome is unknown", m.ToolResult.Error.Message)
	}
	if c1 := toolMessage(sess, "c1"); c1 == nil || c1.ToolResult == nil || !c1.ToolResult.Success {
		t.Errorf("c1 must be backfilled from its recorded success: %+v", c1)
	}

	// The decision is recorded as a result event, so a second resume would see a known result.
	// (The finished turn's events are pruned from the live store; read what was ever recorded.)
	_, res := findEventPair(recorder.AllRecordedEvents("s-unknown-fail"), 0, 1)
	if res == nil || !strings.Contains(string(res.Payload), "outcome_unknown") {
		t.Errorf("no recorded outcome_unknown result for c2: %+v", res)
	}
}

func TestResumeSession_UnknownOutcomeStopIsTheDefault(t *testing.T) {
	for _, policy := range []UnknownOutcomePolicy{"", UnknownOutcomeStop} {
		ag, recorder, tool := unknownOutcomeAgent(t, policy)
		threeCallBatch(t, ag, recorder, "s-unknown-stop")

		_, err := ag.ResumeSession(context.Background(), "s-unknown-stop")
		var gap *ExecutionGap
		if !errors.As(err, &gap) || gap.ToolUseID != "c2" {
			t.Fatalf("policy %q: ResumeSession = %v, want an ExecutionGap for c2", policy, err)
		}
		if tool.calls != 0 {
			t.Errorf("policy %q: tool ran %d times, want 0", policy, tool.calls)
		}
	}
}

func TestUnknownOutcomePolicy_Valid(t *testing.T) {
	for p, want := range map[UnknownOutcomePolicy]bool{"": true, "stop": true, "fail": true, "retry": false, "FAIL": false} {
		if got := p.Valid(); got != want {
			t.Errorf("%q.Valid() = %v, want %v", p, got, want)
		}
	}
}
