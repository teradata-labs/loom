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

import (
	"context"
	"errors"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	luastore "github.com/teradata-labs/loom/pkg/luasandbox/store"
)

// luaScriptOwnerRPC is the owner recorded for scripts saved over RPC.
const luaScriptOwnerRPC = "server"

// SetLuaScriptStore sets the store behind the Lua script RPCs. Leave it unset
// (nil) when tools.lua is disabled; the RPCs then return FailedPrecondition.
func (s *MultiAgentServer) SetLuaScriptStore(st luastore.ScriptStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.luaScripts = st
}

func (s *MultiAgentServer) luaScriptStore() (luastore.ScriptStore, error) {
	s.mu.RLock()
	st := s.luaScripts
	s.mu.RUnlock()
	if st == nil {
		return nil, status.Error(codes.FailedPrecondition, "Lua scripts are disabled on this server (tools.lua.enabled)")
	}
	return st, nil
}

// luaStoreError maps a store error to a gRPC status.
func luaStoreError(err error) error {
	switch {
	case errors.Is(err, luastore.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, luastore.ErrDuplicate):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, luastore.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, luastore.ErrNotPublished):
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	return status.Errorf(codes.Internal, "lua script store: %v", err)
}

// ListLuaScripts lists saved scripts without their source.
func (s *MultiAgentServer) ListLuaScripts(ctx context.Context, _ *loomv1.ListLuaScriptsRequest) (*loomv1.ListLuaScriptsResponse, error) {
	st, err := s.luaScriptStore()
	if err != nil {
		return nil, err
	}
	all, err := st.List(ctx)
	if err != nil {
		return nil, luaStoreError(err)
	}
	out := make([]*loomv1.LuaScript, 0, len(all))
	for _, sc := range all {
		p, err := luaScriptToProto(ctx, st, sc, false)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return &loomv1.ListLuaScriptsResponse{Scripts: out}, nil
}

// GetLuaScript reads one saved script with its source.
func (s *MultiAgentServer) GetLuaScript(ctx context.Context, req *loomv1.GetLuaScriptRequest) (*loomv1.GetLuaScriptResponse, error) {
	if err := luastore.ValidateName(req.GetName()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	st, err := s.luaScriptStore()
	if err != nil {
		return nil, err
	}
	sc, err := st.Get(ctx, req.GetName())
	if err != nil {
		return nil, luaStoreError(err)
	}
	p, err := luaScriptToProto(ctx, st, sc, true)
	if err != nil {
		return nil, err
	}
	return &loomv1.GetLuaScriptResponse{Script: p}, nil
}

// SaveLuaScript validates and saves a script. The source must compile; it is
// never run here.
func (s *MultiAgentServer) SaveLuaScript(ctx context.Context, req *loomv1.SaveLuaScriptRequest) (*loomv1.SaveLuaScriptResponse, error) {
	st, err := s.luaScriptStore()
	if err != nil {
		return nil, err
	}
	saved, created, err := st.Save(ctx, luastore.Script{
		Name:        req.GetName(),
		Description: req.GetDescription(),
		Source:      req.GetSource(),
		Manifest:    luaManifestFromProto(req.GetManifest()),
		Owner:       luaScriptOwnerRPC,
	}, req.GetOverwrite())
	if err != nil {
		return nil, luaStoreError(err)
	}
	p, err := luaScriptToProto(ctx, st, saved, true)
	if err != nil {
		return nil, err
	}
	if s.logger != nil {
		s.logger.Info("SaveLuaScript completed", zap.String("name", saved.Name), zap.Int("version", saved.Version), zap.Bool("created", created))
	}
	return &loomv1.SaveLuaScriptResponse{Script: p, Created: created}, nil
}

// DeleteLuaScript deletes a saved script and its attachments.
func (s *MultiAgentServer) DeleteLuaScript(ctx context.Context, req *loomv1.DeleteLuaScriptRequest) (*loomv1.DeleteLuaScriptResponse, error) {
	if err := luastore.ValidateName(req.GetName()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	st, err := s.luaScriptStore()
	if err != nil {
		return nil, err
	}
	if err := st.Delete(ctx, req.GetName()); err != nil {
		return nil, luaStoreError(err)
	}
	if s.logger != nil {
		s.logger.Info("DeleteLuaScript completed", zap.String("name", req.GetName()))
	}
	return &loomv1.DeleteLuaScriptResponse{Deleted: true}, nil
}

func luaScriptToProto(ctx context.Context, st luastore.ScriptStore, sc luastore.Script, withSource bool) (*loomv1.LuaScript, error) {
	agents, err := st.AttachedAgents(ctx, sc.Name)
	if err != nil {
		return nil, luaStoreError(err)
	}
	p := &loomv1.LuaScript{
		Name:           sc.Name,
		Description:    sc.Description,
		Version:        int32(min(sc.Version, 1<<31-1)), // #nosec G115 -- clamped to int32
		Published:      sc.Published,
		Owner:          sc.Owner,
		AttachedAgents: agents,
	}
	if withSource {
		p.Source = sc.Source
	}
	if !sc.UpdatedAt.IsZero() {
		p.UpdatedAt = timestamppb.New(sc.UpdatedAt)
	}
	if m := sc.Manifest; m != nil {
		pm := &loomv1.LuaScriptManifest{Requires: m.Requires, Returns: m.Returns}
		if m.Parameters != nil {
			params, err := structpb.NewStruct(m.Parameters)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "lua script %s: manifest parameters: %v", sc.Name, err)
			}
			pm.Parameters = params
		}
		p.Manifest = pm
	}
	return p, nil
}

// luaManifestFromProto converts a request manifest. An absent manifest is
// nil. The proto cannot tell an empty requires list from an absent one, so an
// empty list means no restriction.
func luaManifestFromProto(m *loomv1.LuaScriptManifest) *luastore.Manifest {
	if m == nil {
		return nil
	}
	out := &luastore.Manifest{Returns: m.GetReturns()}
	if len(m.GetRequires()) > 0 {
		out.Requires = append([]string(nil), m.GetRequires()...)
	}
	if m.GetParameters() != nil {
		out.Parameters = m.GetParameters().AsMap()
	}
	return out
}
