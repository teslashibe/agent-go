package store

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// verifySchemaRejection fails closed on unknown events, tool calls, assistant
// output, mismatched sessions/prompts, incomplete turns, or non-schema errors.
func verifySchemaRejection(transcript []byte, session, prompt string) error {
	if session == "" || prompt == "" {
		return ErrUncertain
	}
	all := json.NewDecoder(bytes.NewReader(transcript))
	var meta json.RawMessage
	var latest []json.RawMessage
	for {
		var raw json.RawMessage
		if err := all.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			return ErrUncertain
		}
		var header struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
			} `json:"payload"`
		}
		if json.Unmarshal(raw, &header) != nil {
			return ErrUncertain
		}
		if header.Type == "session_meta" {
			meta = raw
		}
		if header.Type == "event_msg" && header.Payload.Type == "task_started" {
			latest = nil
		}
		latest = append(latest, raw)
	}
	if len(meta) == 0 {
		return ErrUncertain
	}
	selected := append([]byte{}, meta...)
	selected = append(selected, '\n')
	for _, raw := range latest {
		selected = append(selected, raw...)
		selected = append(selected, '\n')
	}
	decoder := json.NewDecoder(bytes.NewReader(selected))
	matchedSession, active, matchedPrompt, complete := false, false, false, false
	turn := ""
	for {
		var event struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			return ErrUncertain
		}
		var p struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			SessionID string `json:"session_id"`
			TurnID    string `json:"turn_id"`
			Role      string `json:"role"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Item struct {
				Type string `json:"type"`
			} `json:"item"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			LastAgentMessage json.RawMessage `json:"last_agent_message"`
		}
		if json.Unmarshal(event.Payload, &p) != nil {
			return ErrUncertain
		}
		if event.Type == "session_meta" {
			matchedSession = p.ID == session || p.SessionID == session
			continue
		}
		if event.Type == "event_msg" && p.Type == "task_started" {
			active = true
			matchedPrompt = false
			complete = false
			turn = p.TurnID
			continue
		}
		if !active {
			continue
		}
		if complete {
			return ErrUncertain
		}
		switch event.Type {
		case "turn_context":
			if p.TurnID != turn {
				return ErrUncertain
			}
		case "response_item":
			if p.Type != "message" || p.Role != "user" {
				return ErrUncertain
			}
			for _, c := range p.Content {
				if c.Type == "input_text" && (c.Text == prompt || strings.Contains(c.Text, "\nMessage (untrusted content):\n"+prompt+"\nAuthorized shared Notes (names are lookup keys; never choose or emit an ID):")) {
					matchedPrompt = true
				}
			}
		case "event_msg":
			switch p.Type {
			case "item_completed":
				if p.TurnID != turn || p.Item.Type != "UserMessage" {
					return ErrUncertain
				}
			case "token_count":
			case "task_complete":
				var failure struct {
					Status int `json:"status"`
					Error  struct {
						Type  string `json:"type"`
						Code  string `json:"code"`
						Param string `json:"param"`
					} `json:"error"`
				}
				if p.TurnID != turn || (len(p.LastAgentMessage) != 0 && string(p.LastAgentMessage) != "null") || json.Unmarshal([]byte(p.Error.Message), &failure) != nil || failure.Status != 400 || failure.Error.Type != "invalid_request_error" || failure.Error.Code != "invalid_json_schema" || failure.Error.Param != "text.format.schema" {
					return ErrUncertain
				}
				complete = true
			default:
				return ErrUncertain
			}
		default:
			return ErrUncertain
		}
	}
	if !matchedSession || !active || !matchedPrompt || !complete {
		return ErrUncertain
	}
	return nil
}
