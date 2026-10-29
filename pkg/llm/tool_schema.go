package llm

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap"
)

// ToolSchemaPolicy tracks warnings for incompatible tools on one provider client.
// Its zero value is ready for use and safe for concurrent requests.
type ToolSchemaPolicy struct {
	warned sync.Map
}

// InferSchemaType returns a declared type or the legacy object/array/string fallback.
// It does not mutate or traverse the schema; callers control inference at composite nodes.
func InferSchemaType(schema *shuttle.JSONSchema) string {
	if schema == nil {
		return ""
	}
	if schema.Type != "" {
		return schema.Type
	}
	if schema.Properties != nil {
		return "object"
	}
	if schema.Items != nil {
		return "array"
	}
	return "string"
}

// Prepare omits incompatible tools unless strict validation is enabled.
// Providers may additionally disallow keywords that remain at the normalized root.
func (p *ToolSchemaPolicy) Prepare(ctx context.Context, name string, schema *shuttle.JSONSchema, strict bool, disallowedRootKeywords ...string) (map[string]interface{}, bool, error) {
	normalized, err := NormalizeObjectToolSchema(schema)
	if err == nil {
		for _, keyword := range disallowedRootKeywords {
			if _, present := normalized[keyword]; present {
				err = fmt.Errorf("unsupported tool schema root %s; place the constraint inside an object property", keyword)
				break
			}
		}
	}
	keep, err := p.Check(ctx, name, strict, err)
	if !keep {
		return nil, false, err
	}
	return normalized, true, nil
}

// Check applies the omission policy to a provider-specific schema validation error.
func (p *ToolSchemaPolicy) Check(ctx context.Context, name string, strict bool, err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	err = fmt.Errorf("tool %q schema: %w", name, err)
	if strict {
		return false, err
	}
	warningKey := struct {
		tool   string
		reason string
	}{tool: name, reason: err.Error()}
	if _, warned := p.warned.LoadOrStore(warningKey, struct{}{}); !warned {
		zap.L().Warn("omitting tool with incompatible input schema", zap.String("tool", name), zap.Error(err))
	}
	if span := observability.SpanFromContext(ctx); span != nil {
		span.AddEvent("llm.tool.schema.skipped", map[string]interface{}{"tool": name, "reason": err.Error()})
	}
	return false, nil
}

// NormalizeObjectToolSchema losslessly merges object-root allOf schemas.
// Root alternatives and incompatible constraints return errors instead of being dropped.
func NormalizeObjectToolSchema(schema *shuttle.JSONSchema) (map[string]interface{}, error) {
	if err := ValidateToolSchema(schema); err != nil {
		return nil, err
	}
	root := schema.ToToolMap()
	if root == nil {
		return nil, nil
	}
	return normalizeObjectSchemaRoot(root)
}

// ValidateToolSchema rejects null schema nodes without rejecting an absent tool schema.
func ValidateToolSchema(schema *shuttle.JSONSchema) error {
	if schema == nil {
		return nil
	}
	return validateToolSchemaNode(schema, "$")
}

func validateToolSchemaNode(schema *shuttle.JSONSchema, path string) error {
	if schema == nil {
		return fmt.Errorf("tool schema %s is null", path)
	}
	for name, property := range schema.Properties {
		if err := validateToolSchemaNode(property, fmt.Sprintf("%s.properties[%q]", path, name)); err != nil {
			return err
		}
	}
	if schema.Items != nil {
		if err := validateToolSchemaNode(schema.Items, path+".items"); err != nil {
			return err
		}
	}
	for _, composite := range []struct {
		name     string
		branches []*shuttle.JSONSchema
	}{{"anyOf", schema.AnyOf}, {"oneOf", schema.OneOf}, {"allOf", schema.AllOf}} {
		for index, branch := range composite.branches {
			if err := validateToolSchemaNode(branch, fmt.Sprintf("%s.%s[%d]", path, composite.name, index)); err != nil {
				return err
			}
		}
	}
	if schema.Not != nil {
		return validateToolSchemaNode(schema.Not, path+".not")
	}
	return nil
}

func normalizeObjectSchemaRoot(root map[string]interface{}) (map[string]interface{}, error) {
	if schemaType, _ := root["type"].(string); schemaType != "" && schemaType != "object" {
		return nil, fmt.Errorf("tool schema root must be an object, got %q", schemaType)
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		if _, ok := root[keyword]; ok {
			return nil, fmt.Errorf("cannot losslessly adapt tool schema root %s; place alternatives inside an object property", keyword)
		}
	}
	properties, _ := root["properties"].(map[string]interface{})
	if properties == nil {
		properties = make(map[string]interface{})
	}
	required, _ := root["required"].([]string)
	branches, _ := root["allOf"].([]map[string]interface{})
	for index, branch := range branches {
		if branch == nil {
			return nil, fmt.Errorf("tool schema allOf branch %d is nil", index)
		}
		normalized, err := normalizeObjectSchemaRoot(branch)
		if err != nil {
			return nil, fmt.Errorf("tool schema allOf branch %d: %w", index, err)
		}
		for name, property := range normalized["properties"].(map[string]interface{}) {
			if existing, ok := properties[name]; ok && !reflect.DeepEqual(existing, property) {
				property = map[string]interface{}{"allOf": []interface{}{existing, property}}
			}
			properties[name] = property
		}
		fields, _ := normalized["required"].([]string)
		required = appendUniqueRequired(required, fields)
		for keyword, value := range normalized {
			if err := mergeObjectSchemaKeyword(root, keyword, value); err != nil {
				return nil, fmt.Errorf("tool schema allOf branch %d: %w", index, err)
			}
		}
	}
	delete(root, "allOf")
	root["type"] = "object"
	root["properties"] = properties
	if len(required) > 0 {
		root["required"] = required
	} else {
		delete(root, "required")
	}
	return root, nil
}

func mergeObjectSchemaKeyword(root map[string]interface{}, keyword string, value interface{}) error {
	existing, present := root[keyword]
	switch keyword {
	case "type", "properties", "required":
		return nil
	case "not":
		if present {
			root[keyword] = map[string]interface{}{"anyOf": []interface{}{existing, value}}
		} else {
			root[keyword] = value
		}
	case "enum":
		if !present {
			root[keyword] = value
			return nil
		}
		if !reflect.DeepEqual(existing, value) {
			return mergeObjectSchemaKeyword(root, "not", map[string]interface{}{
				"not": map[string]interface{}{"enum": value},
			})
		}
	case "description":
		if present && !reflect.DeepEqual(existing, value) {
			root[keyword] = existing.(string) + "\n" + value.(string)
		} else {
			root[keyword] = value
		}
	case "default":
		if !present {
			root[keyword] = value
		}
	default:
		if present && !reflect.DeepEqual(existing, value) {
			return fmt.Errorf("cannot losslessly merge conflicting root %s", keyword)
		}
		root[keyword] = value
	}
	return nil
}

func appendUniqueRequired(required, fields []string) []string {
	for _, name := range fields {
		if !slices.Contains(required, name) {
			required = append(required, name)
		}
	}
	return required
}
