package agent

import "context"

type redeliveryKeyCtxKey struct{}

// WithRedeliveryKey marks a Chat call as a delivery that may be repeated after
// a failed turn, identified by key. A caller that redelivers the same work
// (for example a queue consumer that requeues messages when Chat fails)
// passes the same key on every attempt, typically derived from the queue
// message IDs.
//
// When the session's last row is a user row written by an earlier attempt
// with the same key, Chat reuses that row instead of appending the prompt a
// second time. Anything else appends as usual: a call without a key, a
// different key, or a session whose failed attempt already wrote rows after
// its user row. The key is held only in memory, so a session reloaded from
// the store after a restart appends again.
//
// An empty key leaves the context unchanged.
func WithRedeliveryKey(ctx context.Context, key string) context.Context {
	if key == "" {
		return ctx
	}
	return context.WithValue(ctx, redeliveryKeyCtxKey{}, key)
}

// redeliveryKeyFromContext returns the key set by WithRedeliveryKey, or "".
func redeliveryKeyFromContext(ctx context.Context) string {
	key, _ := ctx.Value(redeliveryKeyCtxKey{}).(string)
	return key
}

// redeliveredUserRow returns the session's last row when it is a user row
// written by an earlier attempt of the same delivery, so a retried Chat can
// reuse it. It reads the raw L1 rows: GetMessages() renders for the LLM.
func redeliveredUserRow(session *Session, key string) (Message, bool) {
	if key == "" {
		return Message{}, false
	}
	msgs := rawSessionMessages(session)
	if len(msgs) == 0 {
		return Message{}, false
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" || last.RedeliveryKey != key {
		return Message{}, false
	}
	return last, true
}
