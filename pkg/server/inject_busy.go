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

package server

// Delivery of messages injected into an agent's session by the multi-agent
// plumbing (a worker's reply to its coordinator, a broadcast). Injection runs
// Chat on a session another turn may already be driving. With an event log
// configured, Chat refuses that with agent.ErrSessionBusy instead of waiting,
// and the refusal happens before the turn touches the conversation, so the
// same prompt can be offered again. Treating it like any other Chat failure
// would acknowledge a message that was never delivered.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/eventlog"
)

// errInjectNotDelivered marks an injection whose prompt never reached the
// agent's conversation: the session stayed busy, or no LLM slot could be taken.
// The wrapped cause says which. A caller with a queue behind the message
// should leave it for redelivery; one without should log the loss.
var errInjectNotDelivered = errors.New("injected message was not delivered")

// errLLMSlotTimeout is the cause when an LLM concurrency slot stayed taken for
// the whole wait.
var errLLMSlotTimeout = errors.New("timed out waiting for an LLM concurrency slot")

// injectBusyPolicy bounds how long an injection waits for a busy session.
type injectBusyPolicy struct {
	initial time.Duration // first wait between attempts
	max     time.Duration // cap on the wait between attempts
	total   time.Duration // give up once the next wait would pass this
}

// defaultInjectBusyPolicy waits long enough to outlast a lease a crashed
// process left behind (it only expires after the lease TTL), plus a margin.
func defaultInjectBusyPolicy() injectBusyPolicy {
	return injectBusyPolicy{
		initial: 250 * time.Millisecond,
		max:     5 * time.Second,
		total:   eventlog.DefaultSessionLeaseTTL + 30*time.Second,
	}
}

// injectPolicy returns the server's policy, defaulting unset fields; tests set
// shorter values.
func (s *MultiAgentServer) injectPolicy() injectBusyPolicy {
	p := s.injectBusy
	d := defaultInjectBusyPolicy()
	if p.initial <= 0 {
		p.initial = d.initial
	}
	if p.max <= 0 {
		p.max = d.max
	}
	if p.total <= 0 {
		p.total = d.total
	}
	return p
}

// injectNotDelivered reports whether err means the injected prompt never
// reached the agent's conversation.
func injectNotDelivered(err error) bool {
	return errors.Is(err, errInjectNotDelivered)
}

// acquireLLMSlot takes one slot of the server's LLM concurrency limit. It waits
// for timeout, or until ctx ends when timeout is zero.
func (s *MultiAgentServer) acquireLLMSlot(ctx context.Context, timeout time.Duration) (release func(), err error) {
	// The semaphore can be swapped by SetLLMConcurrency; release must return
	// the slot to the channel it came from.
	sem := s.llmSemaphore
	var expired <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		expired = timer.C
	}
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-expired:
		return nil, errLLMSlotTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// chatInjected runs one injected prompt through ag.Chat on sessionID, holding
// an LLM slot only while a Chat is running. While the session is busy it waits
// with backoff (outside the slot, so a waiting injection does not starve other
// agents' LLM calls) until the policy's total wait is spent.
//
// waitCtx bounds the waiting (slot and backoff); chatCtx is what the turn
// itself runs under. Callers pass different contexts when the turn must outlive
// a worker shutdown. slotTimeout is how long one slot acquisition may wait; zero
// waits until waitCtx ends.
//
// An error wrapping errInjectNotDelivered means the prompt never reached the
// conversation; any other error comes from a turn that ran.
func (s *MultiAgentServer) chatInjected(waitCtx, chatCtx context.Context, ag *agent.Agent, sessionID, prompt string, slotTimeout time.Duration) error {
	policy := s.injectPolicy()
	deadline := time.Now().Add(policy.total)
	delay := policy.initial
	for {
		release, err := s.acquireLLMSlot(waitCtx, slotTimeout)
		if err != nil {
			return fmt.Errorf("%w: %w", errInjectNotDelivered, err)
		}
		_, err = ag.Chat(chatCtx, sessionID, prompt)
		release()
		if !errors.Is(err, agent.ErrSessionBusy) {
			return err
		}

		if time.Until(deadline) <= delay {
			return fmt.Errorf("%w: %w", errInjectNotDelivered, err)
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-waitCtx.Done():
			timer.Stop()
			return fmt.Errorf("%w: %w", errInjectNotDelivered, err)
		}
		delay = min(delay*2, policy.max)
	}
}
