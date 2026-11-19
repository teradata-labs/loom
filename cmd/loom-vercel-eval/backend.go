package main

import (
	"context"
	"errors"

	"github.com/teradata-labs/loom/pkg/fabric"
)

// errNoBackend is returned by every database call. The eval tasks work on
// files and shell only, so no database backend is attached to the agent.
var errNoBackend = errors.New("no database backend in the vercel gateway eval")

// noBackend satisfies fabric.ExecutionBackend with no data behind it.
type noBackend struct{}

func (noBackend) Name() string { return "none" }

func (noBackend) ExecuteQuery(context.Context, string) (*fabric.QueryResult, error) {
	return nil, errNoBackend
}

func (noBackend) GetSchema(context.Context, string) (*fabric.Schema, error) {
	return nil, errNoBackend
}

func (noBackend) ListResources(context.Context, map[string]string) ([]fabric.Resource, error) {
	return nil, errNoBackend
}

func (noBackend) GetMetadata(context.Context, string) (map[string]interface{}, error) {
	return nil, errNoBackend
}

func (noBackend) Ping(context.Context) error { return nil }

func (noBackend) Capabilities() *fabric.Capabilities { return &fabric.Capabilities{} }

func (noBackend) ExecuteCustomOperation(context.Context, string, map[string]interface{}) (interface{}, error) {
	return nil, errNoBackend
}

func (noBackend) Close() error { return nil }
