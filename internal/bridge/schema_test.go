package bridge

import (
	"encoding/json"
	"testing"
)

func TestActionSchemaStrictContract(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(ActionSchema), &schema); err != nil {
		t.Fatal(err)
	}
	var check func(map[string]any)
	check = func(node map[string]any) {
		if props, ok := node["properties"].(map[string]any); ok {
			if node["additionalProperties"] != false {
				t.Fatal("object must disallow extra properties")
			}
			required, ok := node["required"].([]any)
			if !ok || len(required) != len(props) {
				t.Fatal("required must contain every property")
			}
			seen := map[string]bool{}
			for _, key := range required {
				name, ok := key.(string)
				if !ok || seen[name] || props[name] == nil {
					t.Fatal("invalid required property")
				}
				seen[name] = true
			}
		}
		for _, value := range node {
			switch child := value.(type) {
			case map[string]any:
				check(child)
			case []any:
				for _, item := range child {
					if object, ok := item.(map[string]any); ok {
						check(object)
					}
				}
			}
		}
	}
	check(schema)
	props := schema["properties"].(map[string]any)
	if len(props) != 1 || props["reply"] == nil {
		t.Fatal("final schema exposes executable fields")
	}
	if _, err := decodeAction(`{"reply":"Schema contract test"}`); err != nil {
		t.Fatal(err)
	}
}
