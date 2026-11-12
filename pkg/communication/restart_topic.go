// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package communication

import "context"

// RestartTopicPrefix prefixes the per-run MessageBus topic an iterative
// pipeline listens on for RestartRequest messages.
const RestartTopicPrefix = "workflow.restart."

// RestartTopicFor returns the topic an iterative pipeline run with the given
// workflow id listens on for restart requests when its pattern sets no
// restart_topic. One bus serves every workflow a server runs; a topic per run
// keeps one run's restart request from restarting every other run whose
// stage ids happen to match.
func RestartTopicFor(workflowID string) string {
	return RestartTopicPrefix + workflowID
}

// RestartResponseTopic is where the executor listening on restartTopic
// answers the stage that requested a restart: scoped by the run's topic, so
// a same-named stage in another run never receives another run's verdict.
func RestartResponseTopic(restartTopic, requesterStageID string) string {
	return restartTopic + ".response." + requesterStageID
}

type restartTopicKey struct{}

// WithRestartTopic records on ctx the topic the running iterative pipeline
// listens on, so the publish_restart_request tool an agent calls during a
// stage publishes to that run and no other.
func WithRestartTopic(ctx context.Context, topic string) context.Context {
	return context.WithValue(ctx, restartTopicKey{}, topic)
}

// RestartTopicFromContext returns the restart topic recorded by
// WithRestartTopic, or "" when the turn is not part of an iterative pipeline
// run with restarts enabled.
func RestartTopicFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	topic, _ := ctx.Value(restartTopicKey{}).(string)
	return topic
}
