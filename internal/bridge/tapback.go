package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func tapbackTools() []map[string]any {
	return []map[string]any{{
		"name":        "react",
		"description": "Optional native iMessage tapback on this turn's inbound message. Use only when a tapback is the whole response and reply will be empty. Choose the reaction that matches the message: love, like, dislike, laugh, emphasize, or question. Do not default to like. Skip this tool when writing a text reply. Do not send emoji as chat text.",
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"operation_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				"reaction":     map[string]any{"type": "string", "enum": []string{"love", "like", "dislike", "laugh", "emphasize", "question"}},
			},
			"required": []string{"operation_id", "reaction"},
		},
	}}
}

func (t *notesTurn) react(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		OperationID string `json:"operation_id"`
		Reaction    string `json:"reaction"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return "", err
	}
	reaction := strings.ToLower(strings.TrimSpace(args.Reaction))
	switch reaction {
	case "love", "like", "dislike", "laugh", "emphasize", "question":
	default:
		return "", errors.New("unsupported tapback")
	}
	if strings.TrimSpace(args.OperationID) == "" || len(args.OperationID) > 128 {
		return "", errors.New("invalid operation ID")
	}
	canonical, err := json.Marshal(struct {
		Name string
		Args struct {
			OperationID string `json:"operation_id"`
			Reaction    string `json:"reaction"`
		}
	}{"react", args})
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
		if strings.HasPrefix(cached, "Tapback request accepted: ") {
			t.tapped = true
		}
		return cached, nil
	}
	sendCtx, cancel := reactionContext(ctx)
	submitted, reactErr := t.bridge.messenger.React(sendCtx, t.bridge.config.Source.ChatID, t.jobGUID, reaction)
	cancel()
	text := "Tapback request accepted: " + reaction + "; delivery is not independently verified."
	if reactErr != nil {
		text = "Tapback outcome is unknown; no reaction was confirmed."
	} else if !submitted {
		text = "Tapback skipped: the inbound message is not available to react to."
	} else {
		t.tapped = true
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err = t.bridge.store.CompleteToolOperation(persist, t.bridge.config.Source, t.jobID, args.OperationID, text); err != nil {
		t.uncertain = errors.Join(ErrUncertain, err)
		return text, t.uncertain
	}
	return text, nil
}
