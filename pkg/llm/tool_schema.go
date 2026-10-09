package llm

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// NormalizeObjectToolSchema losslessly merges object-root allOf schemas.
// Root alternatives and incompatible constraints return errors instead of being dropped.
func NormalizeObjectToolSchema(schema *shuttle.JSONSchema) (map[string]interface{}, error) {
	root := schema.ToToolMap()
	if root == nil {
		return nil, nil
	}
	return normalizeObjectSchemaRoot(root)
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
