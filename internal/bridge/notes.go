package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/notes"
	notesmcp "github.com/teslashibe/notes/mcp"
)

type createNotePayload struct {
	Title string   `json:"title"`
	Body  string   `json:"body"`
	Items []string `json:"items"`
}
type editNotePayload struct {
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

func (b *Bridge) EnableFamilyNotes(ctx context.Context, client notes.Client) error {
	return b.enableNotes(ctx, client, false)
}

// EnableOwnerNotes requires an explicit operator grant to the sole DM sender.
func (b *Bridge) EnableOwnerNotes(ctx context.Context, client notes.Client) error {
	return b.enableNotes(ctx, client, true)
}

func (b *Bridge) enableNotes(ctx context.Context, client notes.Client, owner bool) error {
	source := b.config.Source
	if owner {
		if source.Validate() != nil || source.Group || len(source.AllowedSenders) != 1 {
			return errors.New("owner Notes requires one configured DM sender")
		}
	} else if !source.Group || source.ChatID <= 0 || !strings.HasPrefix(source.ChatGUID, "any;+;") || len(source.AllowedSenders) < 2 {
		return errors.New("Notes requires a configured family group")
	}
	if err := b.store.ConfigureNotes(ctx, source, nil); err != nil {
		return err
	}
	b.notes = &client
	b.ownerNotes = owner
	if client.NativeExecutable != "" {
		_ = b.refreshSharedNotes(ctx)
	}
	return nil
}

// Preserve the existing shared/unprotected source filter. Shared alone is not
// evidence of collaboration with this chat; this does not expand that policy.
func sharedFamilyNotes(listed []notes.Note) []store.NoteScope {
	return accessibleNotes(listed, false)
}

func accessibleNotes(listed []notes.Note, owner bool) []store.NoteScope {
	var out []store.NoteScope
	for _, note := range listed {
		if (owner || note.Shared) && !note.PasswordProtected && strings.TrimSpace(note.Name) != "" && note.ID != "" {
			out = append(out, store.NoteScope{ID: note.ID, Title: note.Name, State: "authorized"})
		}
	}
	return out
}
func (b *Bridge) refreshSharedNotes(ctx context.Context) error {
	if b.notes == nil {
		return nil
	}
	listed, err := b.notes.List(ctx)
	if err != nil {
		return err
	}
	return b.store.SyncAuthorizedNotes(ctx, b.config.Source, accessibleNotes(listed, b.ownerNotes))
}
func isNoteAction(action string) bool {
	return slices.Contains([]string{"list_notes", "read_note", "add_note_item", "edit_note_item", "edit_note_text", "check_note_item", "uncheck_note_item", "delete_note", "confirm_delete_note", "create_shared_note", "create_note"}, action)
}

type noteOutcome notesmcp.Outcome

func (o noteOutcome) encoded() string            { return notesmcp.Outcome(o).Encoded() }
func noteMetadata(n notes.Note) notesmcp.Outcome { return notesmcp.Metadata(n) }
func (o *noteOutcome) fail(message string, err error) {
	if errors.Is(err, ErrUncertain) || errors.Is(err, store.ErrUncertain) {
		err = &notes.OperationError{Uncertain: true, Err: err}
	}
	(*notesmcp.Outcome)(o).Fail(message, err)
	if err != nil {
		reason, guidance := noteFailureReason(err)
		slog.Warn("Notes operation failed", "reason", reason)
		if guidance != "" {
			o.Message += "; " + guidance
		}
	}
}

// Compatibility for direct execution tests only; production uses MCP IDs.
func (b *Bridge) performNoteAction(ctx context.Context, jobID int64, action store.Action) (string, error) {
	id := ""
	if action.Action != "list_notes" && action.Action != "create_shared_note" && action.Action != "create_note" {
		target, _, err := b.store.ResolveNote(ctx, b.config.Source, action.NoteName)
		if err != nil {
			return "", err
		}
		id = target.ID
	}
	out, err := b.executeNoteAction(ctx, jobID, action, id, "")
	return out.encoded(), err
}

func (b *Bridge) executeNoteAction(ctx context.Context, jobID int64, action store.Action, noteID, operationID string) (out noteOutcome, err error) {
	out = noteOutcome{Status: "completed", NoteID: noteID}
	if b.notes == nil {
		out.fail("Notes is not enabled for this chat", nil)
		return
	}
	creating := action.Action == "create_note" || action.Action == "create_shared_note"
	if creating && (action.Action == "create_note") != b.ownerNotes {
		out.fail("Note creation mode is not enabled for this chat", nil)
		return
	}
	listed, listErr := b.notes.List(ctx)
	if listErr != nil {
		out.fail("Could not list Notes; no note was changed", listErr)
		return
	}
	if err = b.store.SyncAuthorizedNotes(ctx, b.config.Source, accessibleNotes(listed, b.ownerNotes)); err != nil {
		return
	}
	if action.Action == "list_notes" {
		complete := true
		out.Complete = &complete
		out.Notes = []notesmcp.Outcome{}
		for _, n := range listed {
			if _, lookupErr := b.store.NoteByID(ctx, b.config.Source, n.ID); lookupErr == nil {
				out.Notes = append(out.Notes, noteMetadata(n))
			}
		}
		return
	}
	var target store.NoteScope
	if !creating {
		// A fresh explicit request may reconcile a previously reviewed creation,
		// but cannot replay it or admit an arbitrary private note by ID.
		if b.config.Source.Group && operationID != "" {
			eligible, e := b.store.CanVerifyRecoveredSharing(ctx, b.config.Source, noteID)
			if e != nil {
				return out, e
			}
			if eligible {
				activeShared := slices.ContainsFunc(listed, func(n notes.Note) bool {
					return n.ID == noteID && n.Shared && !n.PasswordProtected
				})
				if !activeShared {
					out.fail("Recovered note is not an active shared note", nil)
					return
				}
				if e = b.notes.VerifyParticipants(ctx, noteID, slices.Clone(b.config.Source.AllowedSenders)); e != nil {
					out.fail("Could not verify recovered note participants; no note was changed", e)
					return
				}
				if e = b.store.ConfirmRecoveredSharing(ctx, b.config.Source, noteID, jobID, operationID); e != nil {
					return out, e
				}
			}
		}
		target, err = b.store.NoteByID(ctx, b.config.Source, noteID)
		if err != nil {
			out.fail("Note ID is missing or outside the current source", nil)
			err = nil
			return
		}
		out.Title = target.Title
	}
	if action.Action == "read_note" {
		out = noteOutcome(notesmcp.Read(ctx, b.notes, notesmcp.Outcome(out)))
		return
	}
	start := func() error {
		if operationID != "" {
			return b.store.StartNativeNoteAction(ctx, b.config.Source, jobID, operationID, action.Action, noteID)
		}
		return b.store.StartNoteAction(ctx, b.config.Source, jobID, action.Action, noteID)
	}
	finish := func(effectErr error) error { return b.finishNoteAction(ctx, jobID, &out, effectErr) }

	if action.Action == "delete_note" || action.Action == "confirm_delete_note" {
		sender, e := b.store.JobSender(ctx, b.config.Source, jobID)
		if e != nil {
			return out, e
		}
		n, e := b.notes.Get(ctx, noteID)
		if e != nil || n.ID != noteID || n.PasswordProtected || n.Name != target.Title {
			out.fail("Delete rejected: exact current note could not be verified", e)
			return
		}
		if action.Action == "delete_note" {
			now := time.Now()
			expires := now.Add(10 * time.Minute)
			err = b.store.SetNoteConfirmation(ctx, b.config.Source, store.NoteConfirmation{Sender: sender, Kind: "delete_note", NoteID: noteID, Title: n.Name, ModifiedAt: n.ModifiedAt, CreatedAt: now})
			if err != nil {
				return
			}
			out.Status = "confirmation_required"
			out.ExpiresAt = &expires
			out.Message = fmt.Sprintf("Confirm moving %q to Recently Deleted within 10 minutes. It will remain recoverable.", n.Name)
			return
		}
		c, e := b.store.NoteConfirmation(ctx, b.config.Source, sender, time.Now())
		if e != nil || c.Kind != "delete_note" || c.NoteID != noteID || c.Title != n.Name || !c.ModifiedAt.Equal(n.ModifiedAt) {
			out.fail("Delete confirmation rejected: missing, expired, changed target, or another sender", nil)
			return
		}
		if e = b.store.ConsumeNoteConfirmation(ctx, b.config.Source, c, time.Now()); e != nil {
			out.fail("Delete confirmation rejected: already consumed or changed", nil)
			return
		}
		return b.deleteConfirmedNote(ctx, jobID, operationID, c.Targets[0])
	}
	if creating {
		var payload createNotePayload
		if e := json.Unmarshal([]byte(action.Text), &payload); e != nil {
			out.fail("Invalid creation payload", e)
			return
		}
		if err = start(); err != nil {
			return
		}
		n, e := b.notes.Create(ctx, payload.Title, payload.Body)
		if n.ID == "" && e == nil {
			e = errors.Join(ErrUncertain, errors.New("creation returned no note ID"))
		}
		out.Creation = "failed"
		var operationErr *notes.OperationError
		if errors.Is(e, ErrUncertain) || (errors.As(e, &operationErr) && operationErr.Uncertain) {
			out.Creation = "uncertain"
		}
		out.Sharing = "not_attempted"
		if n.ID != "" {
			out.NoteID = n.ID
			out.Title = n.Name
		}
		if e == nil {
			out.Creation = "completed"
			persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			e = b.store.RecordCreatedNote(persist, b.config.Source, jobID, n.ID, n.Name)
			if e == nil && b.ownerNotes {
				e = b.store.AuthorizePrivateNote(persist, b.config.Source, n.ID)
			}
			if e == nil && !b.ownerNotes {
				e = b.store.MarkNoteSharing(persist, b.config.Source, n.ID)
			}
			cancel()
			if e != nil {
				e = errors.Join(ErrUncertain, e)
			} else if !b.ownerNotes {
				out.Sharing = "unverified"
				e = b.notes.Share(ctx, n.ID, slices.Clone(b.config.Source.AllowedSenders))
				if e == nil {
					persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					e = b.store.SetNoteShared(persist, b.config.Source, n.ID)
					cancel()
					if e != nil {
						e = errors.Join(ErrUncertain, e)
					}
				}
				if e == nil {
					out.Sharing = "verified"
				}
			}
		}
		err = finish(e)
		return
	}
	if action.Action == "edit_note_text" {
		var edit editNotePayload
		if e := json.Unmarshal([]byte(action.Text), &edit); e != nil {
			out.fail("Invalid edit payload", e)
			return
		}
		if err = start(); err != nil {
			return
		}
		e := b.notes.EditText(ctx, noteID, edit.OldText, edit.NewText)
		if e == nil {
			out.Message = "Exact text replacement verified"
		}
		err = finish(e)
		return
	}
	current, e := b.notes.Checklist(ctx, noteID)
	if e != nil {
		out.fail("Could not read checklist; no note was changed", e)
		return
	}
	text := action.Text
	var edit editNotePayload
	if action.Action == "edit_note_item" {
		if e = json.Unmarshal([]byte(action.Text), &edit); e != nil {
			out.fail("Invalid edit payload", e)
			return
		}
		text = edit.OldText
	}
	change, result := notesmcp.PrepareItem(current, action.Action, text, edit.NewText, notesmcp.Outcome(out))
	out = noteOutcome(result)
	if change == nil {
		return
	}
	if err = start(); err != nil {
		return
	}
	result, e = change.Apply(ctx, b.notes, result)
	out = noteOutcome(result)
	err = finish(e)
	return
}
