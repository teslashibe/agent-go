package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/teslashibe/agent-go/internal/reminders"
	"github.com/teslashibe/agent-go/internal/store"
	notesmcp "github.com/teslashibe/notes/mcp"
)

// NotesRunner installs a native MCP endpoint only for this authenticated turn.
// The endpoint owns authority; neither tool arguments nor the subprocess do.
type NotesRunner interface {
	RunWithNotes(context.Context, string, string, http.Handler) (Result, error)
}

type notesTurn struct {
	bridge       *Bridge
	jobID        int64
	jobGUID      string
	mu           sync.Mutex
	closed       bool
	uncertain    error
	canConfirm   bool
	ctx          context.Context
	tapped       bool
	progressSent int
}

func (t *notesTurn) attachedTapback() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tapped
}

type noteToolArgs = notesmcp.Args

func newNotesTurn(ctx context.Context, b *Bridge, job *store.Job) *notesTurn {
	_, err := b.store.NoteConfirmation(ctx, b.config.Source, job.Sender, time.Now())
	return &notesTurn{bridge: b, jobID: job.ID, jobGUID: job.GUID, canConfirm: err == nil, ctx: ctx}
}

func (t *notesTurn) tools() []map[string]any {
	tools := tapbackTools()
	if t.bridge != nil && t.bridge.config.ProgressUpdates {
		tools = append(tools, progressTools()...)
	}
	if t.bridge != nil && t.bridge.config.Source.Group {
		tools = append(tools, reminders.Tools()...)
	}
	if t.bridge != nil && t.bridge.notes != nil {
		for _, tool := range noteTools() {
			name := tool["name"].(string)
			if (name == "create_note" && !t.bridge.ownerNotes) || (name == "create_shared_note" && t.bridge.ownerNotes) {
				continue
			}
			tools = append(tools, tool)
		}
	}
	if t.bridge != nil && t.bridge.google != nil {
		tools = append(tools, t.bridge.google.tools()...)
	}
	return tools
}

// HandlerHasNotes reports whether the per-turn adapter exposes Notes tools.
func HandlerHasNotes(h http.Handler) bool {
	turn, ok := h.(*notesTurn)
	return ok && turn.bridge != nil && turn.bridge.notes != nil
}

// HandlerHasProgress reports whether the per-turn adapter can text mid-turn.
func HandlerHasProgress(h http.Handler) bool {
	turn, ok := h.(*notesTurn)
	return ok && turn.bridge != nil && turn.bridge.config.ProgressUpdates
}

func (t *notesTurn) close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	return t.uncertain
}

func noteTools() []map[string]any { return notesmcp.Tools() }

func (t *notesTurn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.ctx.Err() != nil || r.Method != http.MethodPost {
		http.Error(w, "turn unavailable", http.StatusGone)
		return
	}
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&req); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any
	var rpcErr any
	switch req.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "harness", "version": "1"}}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": t.tools()}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		err := json.Unmarshal(req.Params, &p)
		var text string
		if err == nil {
			callCtx, cancel := context.WithCancel(r.Context())
			stop := context.AfterFunc(t.ctx, cancel)
			text, err = t.call(callCtx, p.Name, p.Arguments)
			stop()
			cancel()
		}
		if err != nil && text == "" {
			out := noteOutcome{}
			out.fail(err.Error(), err)
			text = out.encoded()
		}
		var outcome noteOutcome
		_ = json.Unmarshal([]byte(text), &outcome)
		result = map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": err != nil || outcome.IsError}
	default:
		rpcErr = map[string]any{"code": -32601, "message": "method not found"}
	}
	w.Header().Set("Content-Type", "application/json")
	response := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rpcErr != nil {
		response["error"] = rpcErr
	} else {
		response["result"] = result
	}
	_ = json.NewEncoder(w).Encode(response)
}

func (t *notesTurn) call(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if strings.HasPrefix(name, "google_") {
		if t.bridge == nil || t.bridge.google == nil {
			return "", errors.New("Google tool unavailable")
		}
		return t.bridge.google.call(ctx, name, raw)
	}
	if reminders.IsTool(name) {
		if t.bridge == nil {
			return "", errors.New("reminder tools are disabled")
		}
		args, err := reminders.Decode(name, raw)
		if err != nil {
			return "", err
		}
		return t.bridge.store.ReminderTool(ctx, t.bridge.config.Source, t.jobID, name, args, time.Now())
	}
	if name == "react" {
		return t.react(ctx, raw)
	}
	if name == "report_progress" {
		if t.bridge == nil || !t.bridge.config.ProgressUpdates {
			return "", errors.New("unknown Notes tool")
		}
		return t.reportProgress(ctx, raw)
	}
	if t.bridge == nil || t.bridge.notes == nil {
		return "", errors.New("unknown Notes tool")
	}
	if (name == "create_note" && !t.bridge.ownerNotes) || (name == "create_shared_note" && t.bridge.ownerNotes) {
		return "", errors.New("Notes creation mode is not enabled for this chat")
	}
	args, err := notesmcp.Decode(name, raw)
	if err != nil {
		return "", err
	}
	canonical, _ := json.Marshal(struct {
		Name string
		Args noteToolArgs
	}{name, args})
	claimed, cached, err := t.bridge.store.ClaimToolOperation(ctx, t.bridge.config.Source, t.jobID, args.OperationID, string(canonical))
	if err != nil {
		if errors.Is(err, store.ErrUncertain) {
			t.uncertain = errors.Join(ErrUncertain, err)
			return t.bridge.store.UncertainNoteResult(ctx, t.bridge.config.Source, t.jobID, args.OperationID), err
		}
		return "", err
	}
	if !claimed {
		return cached, nil
	}
	out := noteOutcome{Status: "completed", NoteID: args.NoteID}
	action := store.Action{Action: name, Text: args.Text}
	switch name {
	case "edit_note_item", "edit_note_text":
		data, _ := json.Marshal(editNotePayload{args.OldText, args.NewText})
		action.Text = string(data)
	case "create_shared_note", "create_note":
		data, _ := json.Marshal(createNotePayload{Title: args.Title, Body: args.Body})
		action.Text = string(data)
	}
	if name == "confirm_delete_note" && !t.canConfirm {
		out.fail("deletion requires confirmation from a subsequent user turn", nil)
	} else if name != "add_note_items" {
		out, err = t.perform(ctx, args.OperationID, 0, action, args.NoteID)
	}
	if name == "delete_note" {
		t.canConfirm = false
	}
	if name == "add_note_items" || name == "create_shared_note" || name == "create_note" {
		for i, item := range args.Items {
			part := noteOutcome{Status: "not_attempted", NoteID: out.NoteID, Text: item}
			if !out.IsError && err == nil {
				index := i
				if name != "add_note_items" {
					index++
				}
				part, err = t.perform(ctx, args.OperationID, index, store.Action{Action: "add_note_item", Text: item}, out.NoteID)
				if part.Title != "" {
					out.Title = part.Title
				}
				if part.IsError || err != nil {
					out.fail("Batch incomplete", err)
					if part.ReplayUnsafe {
						out.fail("Batch incomplete", ErrUncertain)
					}
				}
			}
			out.Items = append(out.Items, notesmcp.Outcome(part))
		}
	}
	if err != nil {
		out.fail("Notes operation did not complete", err)
	}
	if out.ReplayUnsafe {
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		saveErr := t.bridge.store.SaveUncertainNoteResult(persist, t.bridge.config.Source, t.jobID, args.OperationID, out.encoded())
		cancel()
		t.uncertain = errors.Join(ErrUncertain, err, saveErr)
		return out.encoded(), t.uncertain
	}
	text := out.encoded()
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err = t.bridge.store.CompleteToolOperation(persist, t.bridge.config.Source, t.jobID, args.OperationID, text); err != nil {
		t.uncertain = errors.Join(ErrUncertain, err)
		out.fail("Could not persist tool result", t.uncertain)
		return out.encoded(), t.uncertain
	}
	return text, nil
}

func (t *notesTurn) perform(ctx context.Context, id string, index int, action store.Action, noteID string) (noteOutcome, error) {
	result, err := t.bridge.executeNoteAction(ctx, t.jobID, action, noteID, id)
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if progressErr := t.bridge.store.RecordToolProgress(persist, t.bridge.config.Source, t.jobID, id, index, result.encoded()); progressErr != nil {
		result.fail("Could not persist progress", ErrUncertain)
		return result, errors.Join(ErrUncertain, err, progressErr)
	}
	return result, err
}

// HandlerHasGoogle reports whether this authenticated turn exposes scoped Google tools.
func HandlerHasGoogle(h http.Handler) bool {
	t, ok := h.(*notesTurn)
	return ok && t.bridge != nil && t.bridge.google != nil
}
