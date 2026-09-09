package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const maxProgressUpdates = 12

func progressTools() []map[string]any {
	return []map[string]any{{
		"name":        "report_progress",
		"description": "Send one short iMessage progress update now, without ending the turn. Call after a durable step (issue filed, branch created, tests, PR, or a blocker). One sentence plus any URL. Do not narrate routine tool chatter. Do not repeat the same step.",
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"operation_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				"text":         map[string]any{"type": "string", "minLength": 1, "maxLength": 500},
			},
			"required": []string{"operation_id", "text"},
		},
	}}
}

func (t *notesTurn) reportProgress(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		OperationID string `json:"operation_id"`
		Text        string `json:"text"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return "", err
	}
	text := strings.TrimSpace(args.Text)
	if strings.TrimSpace(args.OperationID) == "" || len(args.OperationID) > 128 {
		return "", errors.New("invalid operation ID")
	}
	if text == "" || strings.ContainsAny(text, "\r\n") || utf8.RuneCountInString(text) > 500 {
		return "", errors.New("progress text must be one nonempty line of at most 500 characters")
	}
	if t.progressSent >= maxProgressUpdates {
		return "Progress update limit reached for this turn.", nil
	}
	canonical, err := json.Marshal(struct {
		Name string
		Args struct {
			OperationID string `json:"operation_id"`
			Text        string `json:"text"`
		}
	}{"report_progress", args})
	if err != nil {
		return "", err
	}
	claimed, cached, err := t.bridge.store.ClaimToolOperation(ctx, t.bridge.config.Source, t.jobID, args.OperationID, string(canonical))
	if err != nil {
		return "", err
	}
	if !claimed {
		if strings.HasPrefix(cached, "tool_error:") {
			return "", errors.New(strings.TrimPrefix(cached, "tool_error:"))
		}
		return cached, nil
	}
	sendCtx, cancel := context.WithTimeout(ctx, time.Minute)
	sendErr := t.bridge.messenger.Send(sendCtx, t.bridge.config.Source.ChatID, text)
	if sendErr == nil {
		sendErr = sendCtx.Err()
	}
	cancel()
	result := "Sent progress update."
	if sendErr != nil {
		result = "Progress text was not sent."
	} else {
		t.progressSent++
		persist, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_ = t.bridge.store.RecordSubmittedReply(persist, t.bridge.config.Source, t.jobID, text)
		persistCancel()
	}
	complete, completeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer completeCancel()
	_ = t.bridge.store.CompleteToolOperation(complete, t.bridge.config.Source, t.jobID, args.OperationID, result)
	return result, nil
}
