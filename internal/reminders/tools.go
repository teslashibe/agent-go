package reminders

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// Args never carries requester authority or historical request text.
type Args struct {
	OperationID string `json:"operation_id"`
	Text        string `json:"text,omitempty"`
	LocalTime   string `json:"local_time,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
	ReminderID  string `json:"reminder_id,omitempty"`
	Question    string `json:"question,omitempty"`
}

func Tools() []map[string]any {
	var tools []map[string]any
	for _, spec := range []struct {
		name, description string
		fields            []string
	}{
		{"list_reminders", "List this requester's reminders. Use returned IDs for cancellation; do not infer ownership from a name in the message.", nil},
		{"create_reminder", "Schedule a reminder for the current requester. Resolve missing or ambiguous details first. Use stored timezone unless explicitly supplied; never infer from host or phone. local_time is YYYY-MM-DDTHH:mm:ss, future and at most 366 days away; DST ambiguity is rejected.", []string{"text", "local_time", "timezone"}},
		{"cancel_reminder", "Cancel a reminder belonging to this requester using its returned ID. A named person is not authority to cancel their reminder.", []string{"reminder_id"}},
		{"get_timezone", "Read this requester's stored timezone before resolving a local reminder time when it is not already in current context.", nil},
		{"set_timezone", "Set this requester's IANA timezone when they explicitly provide or confirm it. Existing reminders retain their scheduled times.", []string{"timezone"}},
		{"set_pending_reminder", "Save this requester's reminder request when clarification is needed. Present the question to the user; do not schedule until details are resolved. Existing unexpired original request is retained.", []string{"question"}},
		{"clear_pending_reminder", "Clear this requester's pending reminder clarification when they abandon it or no longer want it. This does not cancel scheduled reminders.", nil},
	} {
		props := map[string]any{"operation_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}
		required := []string{"operation_id"}
		for _, field := range spec.fields {
			property := map[string]any{"type": "string"}
			switch field {
			case "text", "question":
				property["minLength"], property["maxLength"] = 1, 500
			case "local_time":
				property["pattern"] = `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}$`
			case "reminder_id":
				property["pattern"] = `^[1-9][0-9]*$`
			case "timezone":
				property["description"] = "IANA timezone, or UTC"
			}
			props[field] = property
			if field != "timezone" || spec.name != "create_reminder" {
				required = append(required, field)
			}
		}
		tools = append(tools, map[string]any{"name": spec.name, "description": spec.description, "inputSchema": map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}})
	}
	return tools
}

func IsTool(name string) bool {
	for _, tool := range Tools() {
		if tool["name"] == name {
			return true
		}
	}
	return false
}

func Decode(name string, raw json.RawMessage) (Args, error) {
	var args Args
	var schema map[string]any
	for _, tool := range Tools() {
		if tool["name"] == name {
			schema = tool["inputSchema"].(map[string]any)
		}
	}
	if schema == nil || !utf8.Valid(raw) {
		return args, errors.New("invalid reminder tool")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return args, errors.New("expected arguments object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return args, err
		}
		key, ok := keyToken.(string)
		if !ok || seen[key] || schema["properties"].(map[string]any)[key] == nil {
			return args, errors.New("unexpected or duplicate argument")
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return args, err
		}
		if len(value) == 0 || value[0] != '"' {
			return args, errors.New("arguments must be strings")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return args, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return args, errors.New("trailing arguments")
	}
	for _, key := range schema["required"].([]string) {
		if !seen[key] {
			return args, errors.New("missing argument: " + key)
		}
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return args, err
	}
	return args, Validate(name, args)
}

func Validate(name string, args Args) error {
	if !IsTool(name) {
		return errors.New("invalid reminder tool")
	}
	if strings.TrimSpace(args.OperationID) == "" || len(args.OperationID) > 128 || strings.ContainsAny(args.OperationID, "\x00\r\n") {
		return errors.New("invalid operation ID")
	}
	if name == "cancel_reminder" {
		if err := ValidateReminderID(args.ReminderID); err != nil {
			return err
		}
	}
	if name == "set_pending_reminder" {
		if _, err := ReminderText(args.Question); err != nil {
			return err
		}
	}
	return nil
}
