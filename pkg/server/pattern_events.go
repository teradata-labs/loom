// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"sync"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// PatternEventBroadcaster broadcasts pattern update events to multiple subscribers.
// Thread-safe for concurrent subscribe/unsubscribe/broadcast operations.
type PatternEventBroadcaster struct {
	mu          sync.RWMutex
	subscribers map[chan *loomv1.PatternUpdateEvent]struct{}
}

// NewPatternEventBroadcaster creates a new pattern event broadcaster.
func NewPatternEventBroadcaster() *PatternEventBroadcaster {
	return &PatternEventBroadcaster{
		subscribers: make(map[chan *loomv1.PatternUpdateEvent]struct{}),
	}
}

// Subscribe registers a new subscriber and returns a channel for receiving events.
// The channel buffer size is 100 to handle bursts of events.
// Caller must call Unsubscribe() to clean up.
func (b *PatternEventBroadcaster) Subscribe() chan *loomv1.PatternUpdateEvent {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan *loomv1.PatternUpdateEvent, 100)
	b.subscribers[ch] = struct{}{}
	return ch
}

// Unsubscribe removes a subscriber and closes their channel.
func (b *PatternEventBroadcaster) Unsubscribe(ch chan *loomv1.PatternUpdateEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.subscribers[ch]; ok {
		delete(b.subscribers, ch)
		close(ch)
	}
}

// Broadcast sends an event to all subscribers.
// Non-blocking: if a subscriber's channel is full, the event is dropped for that subscriber.
func (b *PatternEventBroadcaster) Broadcast(event *loomv1.PatternUpdateEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for ch := range b.subscribers {
		select {
		case ch <- event:
			// Event sent successfully
		default:
			// Channel full, drop event (prevents blocking broadcaster)
		}
	}
}

// BroadcastPatternCreated broadcasts a pattern creation event.
func (b *PatternEventBroadcaster) BroadcastPatternCreated(agentID, patternName, category, filePath string) {
	event := &loomv1.PatternUpdateEvent{
		Type:        loomv1.PatternUpdateType_PATTERN_CREATED,
		AgentId:     agentID,
		PatternName: patternName,
		Category:    category,
		Timestamp:   time.Now().UnixMilli(),
		FilePath:    filePath,
	}
	b.Broadcast(event)
}

// BroadcastPatternModified broadcasts a pattern modification event.
func (b *PatternEventBroadcaster) BroadcastPatternModified(agentID, patternName, category, filePath string) {
	event := &loomv1.PatternUpdateEvent{
		Type:        loomv1.PatternUpdateType_PATTERN_MODIFIED,
		AgentId:     agentID,
		PatternName: patternName,
		Category:    category,
		Timestamp:   time.Now().UnixMilli(),
		FilePath:    filePath,
	}
	b.Broadcast(event)
}

// BroadcastPatternDeleted broadcasts a pattern deletion event.
func (b *PatternEventBroadcaster) BroadcastPatternDeleted(agentID, patternName, category string) {
	event := &loomv1.PatternUpdateEvent{
		Type:        loomv1.PatternUpdateType_PATTERN_DELETED,
		AgentId:     agentID,
		PatternName: patternName,
		Category:    category,
		Timestamp:   time.Now().UnixMilli(),
	}
	b.Broadcast(event)
}

// BroadcastPatternValidationFailed broadcasts a pattern validation failure event.
func (b *PatternEventBroadcaster) BroadcastPatternValidationFailed(agentID, patternName, errorMsg string) {
	event := &loomv1.PatternUpdateEvent{
		Type:        loomv1.PatternUpdateType_PATTERN_VALIDATION_FAILED,
		AgentId:     agentID,
		PatternName: patternName,
		Timestamp:   time.Now().UnixMilli(),
		Error:       errorMsg,
	}
	b.Broadcast(event)
}

// Close closes all subscriber channels.
func (b *PatternEventBroadcaster) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	for ch := range b.subscribers {
		close(ch)
	}
	b.subscribers = make(map[chan *loomv1.PatternUpdateEvent]struct{})
}
