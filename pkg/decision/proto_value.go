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

package decision

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"
)

// protoreflectMessage is the return type of ProtoReflect on every generated
// message; naming it keeps the type switch in ToValue readable.
type protoreflectMessage = protoreflect.Message

// protoToValue converts a proto message to a structpb.Value via protojson so
// generated types (a tool schema, a skill manifest) can be passed as state or
// criteria without hand-flattening.
func protoToValue(m interface{ ProtoReflect() protoreflectMessage }) (*structpb.Value, error) {
	pm, ok := m.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("decision: %T is not a proto.Message", m)
	}
	raw, err := protojson.Marshal(pm)
	if err != nil {
		return nil, fmt.Errorf("decision: marshal %T: %w", m, err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, fmt.Errorf("decision: %T JSON round trip: %w", m, err)
	}
	return structpb.NewValue(generic)
}
