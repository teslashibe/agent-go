package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/harness"
	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/notes"
)

type nativeTestRunner struct {
	handler   http.Handler
	runNative func(context.Context, http.Handler) (Result, error)
}

func (r *nativeTestRunner) Run(context.Context, string, string) (Result, error) {
	return Result{}, errors.New("legacy runner used")
}
func (r *nativeTestRunner) RunWithNotes(ctx context.Context, _, prompt string, h http.Handler) (Result, error) {
	r.handler = h
	if strings.Contains(prompt, "Required action contract") {
		return Result{}, errors.New("legacy contract leaked")
	}
	return r.runNative(ctx, h)
}

func rpc(t *testing.T, h http.Handler, name string, args string) string {
	t.Helper()
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest("POST", "/mcp", strings.NewReader(request)))
	return recorder.Body.String()
}

func TestNativeNotesRoundtripAndFinalResponse(t *testing.T) {
	b, _, _, messenger := familyNotesBridge(t)
	client := &fakeNotes{}
	b.notes = client
	runner := &nativeTestRunner{}
	runner.runNative = func(ctx context.Context, h http.Handler) (Result, error) {
		path, closeEndpoint, err := harness.ServeEndpoint(h)
		if err != nil {
			t.Fatal(err)
		}
		defer closeEndpoint()
		input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"add_note_items","arguments":{"operation_id":"groceries","note_id":"shopping-id","items":["Pizzas","Pita breads","Walnuts"]}}}` + "\n" +
			`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read_note","arguments":{"operation_id":"read","note_id":"shopping-id"}}}` + "\n"
		var output bytes.Buffer
		if err := harness.RelayMCP(ctx, path, strings.NewReader(input), &output); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(output.String()), "\n")
		if len(lines) != 4 {
			t.Fatalf("responses: %s", output.String())
		}
		for _, line := range lines {
			var v map[string]any
			if err := json.Unmarshal([]byte(line), &v); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(line, `"isError":true`) {
				t.Fatal(line)
			}
		}
		if !strings.Contains(lines[1], "add_note_items") || !strings.Contains(lines[3], "checklist_available") {
			t.Fatal(output.String())
		}
		duplicate := rpc(t, h, "add_note_items", `{"operation_id":"groceries","note_id":"shopping-id","items":["Pizzas","Pita breads","Walnuts"]}`)
		if strings.Contains(duplicate, `"isError":true`) || client.adds != 3 {
			t.Fatalf("duplicate=%s adds=%d", duplicate, client.adds)
		}
		changed := rpc(t, h, "add_note_items", `{"operation_id":"groceries","note_id":"shopping-id","items":["Milk"]}`)
		if !strings.Contains(changed, "different arguments") {
			t.Fatal(changed)
		}
		forged := rpc(t, h, "read_note", `{"operation_id":"forged","note_id":"shopping-id","source":"another"}`)
		if !strings.Contains(forged, "unexpected argument") {
			t.Fatal(forged)
		}
		return Result{SessionID: "native-session", Text: actionText(store.Action{Action: "none", Reply: "Added pizzas, pita breads and walnuts."})}, nil
	}
	b.runner = runner
	intake(t, b, noteMessage(b, 50, "Add pizzas and pita breads plus walnuts to our shopping list in notes"), "turn")
	drain(t, b)
	if client.adds != 3 || len(messenger.texts) != 1 || messenger.texts[0] != "Added pizzas, pita breads and walnuts." {
		t.Fatalf("adds=%d replies=%v", client.adds, messenger.texts)
	}
	recorder := httptest.NewRecorder()
	runner.handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusGone {
		t.Fatalf("stale endpoint status=%d", recorder.Code)
	}
}

type safeReadFailure struct{ fakeNotes }

func (f *safeReadFailure) Get(context.Context, string) (notes.Note, error) {
	return notes.Note{}, errors.New("fixture safe read failure")
}
func TestNativeSafeErrorRecovery(t *testing.T) {
	b, s, _, _ := familyNotesBridge(t)
	b.notes = &safeReadFailure{}
	intake(t, b, noteMessage(b, 90, "read"), "turn")
	job, _, err := s.ClaimNext(context.Background(), b.config.Source, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	turn := newNotesTurn(context.Background(), b, job)
	got := rpc(t, turn, "read_note", `{"operation_id":"read","note_id":"shopping-id"}`)
	if !strings.Contains(got, "Could not read") {
		t.Fatal(got)
	}
	got = rpc(t, turn, "list_notes", `{"operation_id":"recover"}`)
	if strings.Contains(got, `"isError":true`) || turn.uncertain != nil {
		t.Fatal(got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recorder := httptest.NewRecorder()
	newNotesTurn(ctx, b, job).ServeHTTP(recorder, httptest.NewRequest("POST", "/", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusGone {
		t.Fatal(recorder.Code)
	}
}

type safeWriteFailure struct{ fakeNotes }

func (f *safeWriteFailure) AddChecklistItem(context.Context, string, string) ([]notes.ChecklistItem, error) {
	f.adds++
	if f.adds == 1 {
		return nil, &notes.OperationError{Operation: "add", Err: errors.New("fixture rejected before write")}
	}
	return []notes.ChecklistItem{{Text: "recovered"}}, nil
}
func TestNativeSafeWriteRecovery(t *testing.T) {
	b, s, _, _ := familyNotesBridge(t)
	client := &safeWriteFailure{}
	b.notes = client
	intake(t, b, noteMessage(b, 91, "add"), "turn")
	job, _, err := s.ClaimNext(context.Background(), b.config.Source, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	turn := newNotesTurn(context.Background(), b, job)
	got := rpc(t, turn, "add_note_items", `{"operation_id":"first","note_id":"shopping-id","items":["one"]}`)
	if !strings.Contains(got, "Could not verify") || turn.uncertain != nil {
		t.Fatal(got)
	}
	got = rpc(t, turn, "add_note_items", `{"operation_id":"second","note_id":"shopping-id","items":["two"]}`)
	if strings.Contains(got, `"isError":true`) || client.adds != 2 {
		t.Fatal(got)
	}
}

type uncertainNativeNotes struct{ fakeNotes }

type textEditNotes struct {
	fakeNotes
	calls                int
	id, oldText, newText string
	err                  error
}

func (f *textEditNotes) EditText(_ context.Context, id, oldText, newText string) error {
	f.calls++
	f.id, f.oldText, f.newText = id, oldText, newText
	return f.err
}

func TestNativeTextEditAuthorizationAndReplay(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertain), func(t *testing.T) {
			b, s, _, _ := familyNotesBridge(t)
			client := &textEditNotes{}
			if uncertain {
				client.err = &notes.OperationError{Operation: "edit_text", Uncertain: true}
			}
			b.notes = client
			intake(t, b, noteMessage(b, 92, "edit text"), "turn")
			job, _, err := s.ClaimNext(context.Background(), b.config.Source, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			turn := newNotesTurn(context.Background(), b, job)
			outside := rpc(t, turn, "edit_note_text", `{"operation_id":"outside","note_id":"private-id","old_text":"secret","new_text":""}`)
			if !strings.Contains(outside, "outside the current source") || client.calls != 0 {
				t.Fatal(outside)
			}
			args := `{"operation_id":"edit","note_id":"shopping-id","old_text":"First line.\nSecond line.","new_text":""}`
			got := rpc(t, turn, "edit_note_text", args)
			out := decodeOutcome(t, got)
			if client.calls != 1 || client.id != "shopping-id" || client.oldText != "First line.\nSecond line." || client.newText != "" || out.ReplayUnsafe != uncertain || out.IsError != uncertain {
				t.Fatalf("result=%s calls=%d", got, client.calls)
			}
			if retry := rpc(t, turn, "edit_note_text", args); retry != got || client.calls != 1 {
				t.Fatalf("retry=%s", retry)
			}
			if uncertain {
				rpc(t, turn, "edit_note_text", strings.Replace(args, `"edit"`, `"retry-new-id"`, 1))
				if client.calls != 1 || turn.uncertain == nil {
					t.Fatal("uncertain edit was replayed")
				}
			}
		})
	}
}

func (f *uncertainNativeNotes) AddChecklistItem(context.Context, string, string) ([]notes.ChecklistItem, error) {
	f.adds++
	if f.adds == 2 {
		return nil, &notes.OperationError{Uncertain: true}
	}
	return []notes.ChecklistItem{{Text: "first"}}, nil
}
func TestNativeBatchUncertaintyFreezesNewIDs(t *testing.T) {
	b, s, _, messenger := familyNotesBridge(t)
	client := &uncertainNativeNotes{}
	b.notes = client
	b.runner = &nativeTestRunner{runNative: func(ctx context.Context, h http.Handler) (Result, error) {
		got := rpc(t, h, "add_note_items", `{"operation_id":"batch","note_id":"shopping-id","items":["first","second","third"]}`)
		if !strings.Contains(got, `"isError":true`) {
			t.Fatal(got)
		}
		if retry := rpc(t, h, "add_note_items", `{"operation_id":"batch","note_id":"shopping-id","items":["first","second","third"]}`); retry != got {
			t.Fatalf("uncertain retry lost outcome: %s / %s", got, retry)
		}
		out := decodeOutcome(t, got)
		if len(out.Items) != 3 || out.Items[0].Status != "completed" || out.Items[1].Status != "uncertain" || out.Items[2].Status != "not_attempted" {
			t.Fatal(got)
		}
		got = rpc(t, h, "add_note_items", `{"operation_id":"new-id","note_id":"shopping-id","items":["fourth"]}`)
		if !strings.Contains(got, `"isError":true`) || client.adds != 2 {
			t.Fatalf("response=%s adds=%d", got, client.adds)
		}
		return Result{Text: actionText(store.Action{Action: "none", Reply: "All done"})}, nil
	}}
	intake(t, b, noteMessage(b, 51, "add these items"), "turn")
	_, err := b.ProcessNext(context.Background())
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("err=%v", err)
	}
	status, err := s.Status(context.Background(), b.config.Source)
	if err != nil || status.UnknownTurns != 1 || len(messenger.texts) != 0 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestNativeNotesRequiresBoundRunningJobAndLaterConfirmation(t *testing.T) {
	b, s, _, _ := familyNotesBridge(t)
	b.notes = &fakeNotes{}
	intake(t, b, noteMessage(b, 52, "read notes"), "turn")
	job, _, err := s.ClaimNext(context.Background(), b.config.Source, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	turn := newNotesTurn(context.Background(), b, job)
	got := rpc(t, turn, "confirm_delete_note", `{"operation_id":"delete","note_id":"shopping-id"}`)
	if !strings.Contains(got, "subsequent user turn") {
		t.Fatal(got)
	}
	other := &Bridge{store: b.store, notes: b.notes, config: b.config}
	other.config.Source.Name = "wrong-source"
	got = rpc(t, newNotesTurn(context.Background(), other, job), "list_notes", `{"operation_id":"list"}`)
	if !strings.Contains(got, `"isError":true`) {
		t.Fatal(got)
	}
	got = rpc(t, turn, "read_note", `{"operation_id":"personal","note_id":"private-id"}`)
	if !strings.Contains(got, "outside the current source") {
		t.Fatal(got)
	}
}

type recordingNotes struct {
	fakeNotes
	adds []string
}

func (r *recordingNotes) List(context.Context) ([]notes.Note, error) {
	return []notes.Note{
		{ID: "p10", Name: "Shopping List", Shared: true},
		{ID: "p19", Name: "Farmers Market", Shared: true},
	}, nil
}

func (r *recordingNotes) AddChecklistItem(_ context.Context, id, text string) ([]notes.ChecklistItem, error) {
	r.adds = append(r.adds, id)
	return []notes.ChecklistItem{{Text: text}}, nil
}

func TestHarnessPreservesAgentSelectedID(t *testing.T) {
	b, s, _, _ := familyNotesBridge(t)
	client := &recordingNotes{}
	b.notes = client
	intake(t, b, noteMessage(b, 60, "Add thyme and carrots to grocery list"), "turn")
	job, _, err := s.ClaimNext(context.Background(), b.config.Source, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := rpc(t, newNotesTurn(context.Background(), b, job), "add_note_items", `{"operation_id":"misrouted","note_id":"p19","items":["thyme"]}`)
	if strings.Contains(got, `"isError":true`) {
		t.Fatal(got)
	}
	if len(client.adds) != 1 || client.adds[0] != "p19" {
		t.Fatalf("wrote %v, want Farmers Market p19", client.adds)
	}
}

func decodeOutcome(t *testing.T, response string) noteOutcome {
	t.Helper()
	var envelope struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(response), &envelope); err != nil {
		t.Fatal(err)
	}
	var out noteOutcome
	if len(envelope.Result.Content) != 1 {
		t.Fatal(response)
	}
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &out); err != nil {
		t.Fatal(response, err)
	}
	if envelope.Result.IsError != out.IsError {
		t.Fatal("inconsistent isError", response)
	}
	return out
}

func TestNativeCachedFailurePreservesErrorAndBatchProgress(t *testing.T) {
	b, s, _, _ := familyNotesBridge(t)
	client := &safeWriteFailure{}
	b.notes = client
	intake(t, b, noteMessage(b, 101, "add"), "turn")
	job, _, err := s.ClaimNext(context.Background(), b.config.Source, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	turn := newNotesTurn(context.Background(), b, job)
	args := `{"operation_id":"batch","note_id":"shopping-id","items":["one","two"]}`
	first := rpc(t, turn, "add_note_items", args)
	out := decodeOutcome(t, first)
	if !out.IsError || len(out.Items) != 2 || out.Items[0].Status != "failed" || out.Items[1].Status != "not_attempted" {
		t.Fatal(first)
	}
	if retry := rpc(t, turn, "add_note_items", args); retry != first || client.adds != 1 {
		t.Fatal(first, retry, client.adds)
	}
}

type creationNotes struct {
	fakeNotes
	ids      []string
	shareErr error
}

func (c *creationNotes) List(context.Context) ([]notes.Note, error) {
	listed := []notes.Note{{ID: "other-id", Name: "Duplicate", Shared: true}}
	if c.creates > 0 {
		listed = append(listed, notes.Note{ID: "created-id", Name: "Duplicate", Shared: true})
	}
	return listed, nil
}
func (c *creationNotes) Share(context.Context, string, []string) error { return c.shareErr }
func (c *creationNotes) AddChecklistItem(_ context.Context, id, text string) ([]notes.ChecklistItem, error) {
	c.ids = append(c.ids, id)
	return []notes.ChecklistItem{{Text: text}}, nil
}
func TestNativeCreationRetainsIDWithDuplicateTitleAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "sharing failure"}[fail], func(t *testing.T) {
			b, s, _, _ := familyNotesBridge(t)
			client := &creationNotes{}
			if fail {
				client.shareErr = errors.New("share rejected")
			}
			b.notes = client
			intake(t, b, noteMessage(b, 102, "create"), "turn")
			job, _, err := s.ClaimNext(context.Background(), b.config.Source, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			turn := newNotesTurn(context.Background(), b, job)
			args := `{"operation_id":"create","title":"Duplicate","body":"","items":["one"]}`
			first := rpc(t, turn, "create_shared_note", args)
			out := decodeOutcome(t, first)
			if out.NoteID != "created-id" || out.Creation != "completed" || out.IsError != fail {
				t.Fatal(first)
			}
			if !fail && (len(client.ids) != 1 || client.ids[0] != "created-id") {
				t.Fatal(client.ids)
			}
			if fail {
				listed := decodeOutcome(t, rpc(t, turn, "list_notes", `{"operation_id":"list"}`))
				for _, n := range listed.Notes {
					if n.NoteID == "created-id" {
						t.Fatal("recovery ID leaked", listed)
					}
				}
			}
			if retry := rpc(t, turn, "create_shared_note", args); retry != first || client.creates != 1 {
				t.Fatal(retry, client.creates)
			}
		})
	}
}

func TestNativeDiscoveryDuplicateTitlesAndInvalidTargets(t *testing.T) {
	b, s, _, _ := familyNotesBridge(t)
	b.notes = &creationNotes{fakeNotes: fakeNotes{creates: 1}}
	intake(t, b, noteMessage(b, 103, "list"), "turn")
	job, _, err := s.ClaimNext(context.Background(), b.config.Source, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	turn := newNotesTurn(context.Background(), b, job)
	out := decodeOutcome(t, rpc(t, turn, "list_notes", `{"operation_id":"list"}`))
	if out.Complete == nil || !*out.Complete || len(out.Notes) != 2 || out.Notes[0].NoteID == out.Notes[1].NoteID {
		t.Fatal(out)
	}
	for _, args := range []string{`{"operation_id":"old","note_name":"Duplicate"}`, `{"operation_id":"title","note_id":"Duplicate"}`, `{"operation_id":"null","note_id":null}`} {
		if !decodeOutcome(t, rpc(t, turn, "read_note", args)).IsError {
			t.Fatal(args)
		}
	}
}
