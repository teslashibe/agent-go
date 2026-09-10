package reminders

import (
	"encoding/json"
	"testing"
)

func TestToolSchemasAndStrictArguments(t *testing.T) {
	tools := Tools()
	if len(tools) != 7 {
		t.Fatal(len(tools))
	}
	for _, tool := range tools {
		name := tool["name"].(string)
		schema := tool["inputSchema"].(map[string]any)
		if schema["additionalProperties"] != false {
			t.Fatal(name)
		}
		props := schema["properties"].(map[string]any)
		for _, forbidden := range []string{"sender", "user_id", "source", "job_id", "prompt", "original_request"} {
			if props[forbidden] != nil {
				t.Fatal("authority field exposed", forbidden)
			}
		}
		values := map[string]string{"operation_id": "op", "text": "task", "local_time": "2026-09-07T09:00:00", "timezone": "UTC", "reminder_id": "1", "question": "When?", "recipient_id": "sam"}
		args := map[string]string{}
		for key := range props {
			args[key] = values[key]
		}
		raw, _ := json.Marshal(args)
		if _, err := Decode(name, raw); err != nil {
			t.Fatalf("%s %v", name, err)
		}
		args["sender"] = "other"
		raw, _ = json.Marshal(args)
		if _, err := Decode(name, raw); err == nil {
			t.Fatal("authority accepted")
		}
	}
	for _, raw := range []string{`{"operation_id":"a","operation_id":"b"}`, `{"operation_id":null}`, `{"operation_id":""}`, `{"operation_id":"a"} {}`, `[]`} {
		if _, err := Decode("get_timezone", json.RawMessage(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}
