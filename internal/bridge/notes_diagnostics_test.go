package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/teslashibe/notes"
)

type checklistDiagnosticFixture struct {
	fakeNotes
	err    error
	checks int
}

func (f *checklistDiagnosticFixture) Checklist(context.Context, string) ([]notes.ChecklistItem, error) {
	f.checks++
	return nil, f.err
}

func TestChecklistFailureDiagnosticSurvivesMCPAndRetry(t *testing.T) {
	b, _, _, _ := familyNotesBridge(t)
	f := &checklistDiagnosticFixture{err: &notes.OperationError{Operation: "checklist", Err: errors.New("Accessibility permission required")}}
	b.notes = f
	runner := &nativeTestRunner{}
	runner.runNative = func(ctx context.Context, h http.Handler) (Result, error) {
		args := `{"operation_id":"fixture-add","note_id":"shopping-id","items":["Milk","Bread"]}`
		first := rpc(t, h, "add_note_items", args)
		var envelope struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(first), &envelope); err != nil {
			t.Fatal(err)
		}
		if !envelope.Result.IsError || len(envelope.Result.Content) != 1 {
			t.Fatalf("invalid error envelope: %s", first)
		}
		var out noteOutcome
		if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &out); err != nil {
			t.Fatal(err)
		}
		if out.Status != "failed" || out.ReplayUnsafe || len(out.Items) != 2 ||
			!strings.Contains(out.Items[0].Message, "macOS Accessibility permission") || out.Items[1].Status != "not_attempted" {
			t.Fatalf("missing safe diagnosis or batch semantics: %+v", out)
		}
		if repeated := rpc(t, h, "add_note_items", args); repeated != first {
			t.Fatalf("retry changed result: %s", repeated)
		}
		if f.checks != 1 || f.adds != 0 {
			t.Fatalf("checks=%d adds=%d", f.checks, f.adds)
		}
		return Result{SessionID: "fixture-diagnostic", Text: `{"reply":"The Notes helper needs Accessibility permission; no item was added."}`}, nil
	}
	b.runner = runner
	intake(t, b, noteMessage(b, 901, "Add fixture items"), "turn")
	drain(t, b)
}

func TestNoteFailureReasonsAreBoundedAndPreserveUncertainty(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{context.DeadlineExceeded, "timeout"},
		{context.Canceled, "canceled"},
		{notes.ErrUnsupported, "unsupported"},
		{&notes.OperationError{Err: errors.New("Notes automation busy")}, "busy"},
		{&notes.OperationError{Err: errors.New("Notes has a modal dialog; finish it manually")}, "modal_open"},
		{&notes.OperationError{Err: errors.New("ambiguous note editor")}, "editor_unverified"},
		{&notes.OperationError{Err: errors.New("Notes editor unavailable while the Mac is locked")}, "desktop_locked"},
		{&notes.OperationError{Err: errors.New("native checklist state unavailable")}, "checklist_unavailable"},
		{&notes.OperationError{Err: errors.New("Notes lost foreground; keyboard automation stopped")}, "foreground_lost"},
		{&notes.OperationError{Err: errors.New("private note contents: Accessibility permission required")}, "unclassified"},
	} {
		reason, _ := noteFailureReason(fmt.Errorf("connector: %w", tc.err))
		if reason != tc.reason {
			t.Fatalf("reason=%q want=%q", reason, tc.reason)
		}
	}
	out := noteOutcome{}
	out.fail("Could not update note", &notes.OperationError{Uncertain: true, Err: errors.New("private note contents")})
	if out.Status != "uncertain" || !out.ReplayUnsafe || strings.Contains(out.encoded(), "private note contents") {
		t.Fatalf("unsafe diagnostic: %+v", out)
	}
}
