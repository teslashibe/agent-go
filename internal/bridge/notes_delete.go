package bridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/notes"
	notesmcp "github.com/teslashibe/notes/mcp"
)

func matchesDeletion(n notes.Note, target store.NoteDeletionTarget) bool {
	return n.ID == target.NoteID && n.Name == target.Title && !n.PasswordProtected &&
		!n.ModifiedAt.IsZero() && n.ModifiedAt.Equal(target.ModifiedAt)
}

// Prepare every target before requesting or consuming approval. No native write
// happens during preflight; one changed or inaccessible note rejects the set.
func (b *Bridge) deletionTargets(ctx context.Context, ids []string) ([]store.NoteDeletionTarget, error) {
	if err := b.refreshSharedNotes(ctx); err != nil {
		return nil, err
	}
	var targets []store.NoteDeletionTarget
	for _, id := range ids {
		scope, err := b.store.NoteByID(ctx, b.config.Source, id)
		if err != nil {
			return nil, errors.New("note ID is outside the current source")
		}
		n, err := b.notes.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		target := store.NoteDeletionTarget{NoteID: id, Title: scope.Title, ModifiedAt: n.ModifiedAt}
		if !matchesDeletion(n, target) {
			return nil, errors.New("exact current note could not be verified")
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func (t *notesTurn) deleteNotes(ctx context.Context, name string, args noteToolArgs) (out noteOutcome, err error) {
	b := t.bridge
	out.Status = "completed"
	for _, id := range args.NoteIDs {
		out.Notes = append(out.Notes, notesmcp.Outcome{Status: "not_attempted", NoteID: id})
	}
	sender, err := b.store.JobSender(ctx, b.config.Source, t.jobID)
	if err != nil {
		return out, err
	}
	if name == "delete_notes" {
		if err := b.store.ClearNoteConfirmation(ctx, b.config.Source, sender); err != nil {
			return out, err
		}
	}
	var confirmation store.NoteConfirmation
	if name == "confirm_delete_notes" {
		confirmation, err = b.store.NoteConfirmation(ctx, b.config.Source, sender, time.Now())
		var ids []string
		for _, target := range confirmation.Targets {
			ids = append(ids, target.NoteID)
		}
		if err != nil || confirmation.Kind != "delete_notes" || !slices.Equal(ids, args.NoteIDs) {
			out.fail("Batch confirmation rejected: confirm the same complete list from the same requester within 10 minutes", nil)
			return out, nil
		}
	}
	targets, err := b.deletionTargets(ctx, args.NoteIDs)
	if err != nil {
		out.fail("Batch preflight failed; no notes were deleted", err)
		return out, nil
	}
	for i, target := range targets {
		out.Notes[i].Title = target.Title
		stamp := target.ModifiedAt
		out.Notes[i].ModifiedAt = &stamp
	}
	if name == "delete_notes" {
		now := time.Now()
		confirmation = store.NoteConfirmation{Sender: sender, Kind: "delete_notes", Targets: targets, CreatedAt: now}
		if err := b.store.SetNoteConfirmation(ctx, b.config.Source, confirmation); err != nil {
			return out, err
		}
		expires := now.Add(10 * time.Minute)
		out.ExpiresAt = &expires
		out.Status = "confirmation_required"
		out.Message = fmt.Sprintf("Confirm moving all %d listed notes to Recently Deleted within 10 minutes. They remain recoverable; no notes have been deleted.", len(targets))
		return out, nil
	}
	if !slices.EqualFunc(targets, confirmation.Targets, func(a, b store.NoteDeletionTarget) bool {
		return a.NoteID == b.NoteID && a.Title == b.Title && a.ModifiedAt.Equal(b.ModifiedAt)
	}) {
		out.fail("Batch confirmation rejected: a selected note changed; request a new review of the complete list", nil)
		return out, nil
	}
	if err := b.store.ConsumeNoteConfirmation(ctx, b.config.Source, confirmation, time.Now()); err != nil {
		out.fail("Batch confirmation rejected: expired, replaced, or already consumed", nil)
		return out, nil
	}
	completed := 0
	for i, target := range targets {
		part, effectErr := b.deleteConfirmedNote(ctx, t.jobID, args.OperationID, target)
		part, effectErr = t.recordNoteProgress(ctx, args.OperationID, i, part, effectErr)
		out.Notes[i] = notesmcp.Outcome(part)
		if part.IsError || effectErr != nil {
			out.fail("Batch stopped; inspect each note result before making a new request", effectErr)
			if part.ReplayUnsafe {
				out.fail("Batch stopped with an uncertain deletion; automatic replay is unsafe", ErrUncertain)
			} else if completed > 0 {
				out.Status = "partial"
			}
			return out, effectErr
		}
		completed++
	}
	out.Message = fmt.Sprintf("Moved all %d selected notes to Recently Deleted and verified each inactive", completed)
	return out, nil
}

// Both single and batch confirmation paths consume approval before reaching here.
// Recheck each exact target immediately before dispatch; GUI effects are not atomic.
func (b *Bridge) deleteConfirmedNote(ctx context.Context, jobID int64, operationID string, target store.NoteDeletionTarget) (out noteOutcome, err error) {
	out = noteOutcome{Status: "completed", NoteID: target.NoteID, Title: target.Title}
	current, err := b.deletionTargets(ctx, []string{target.NoteID})
	if err != nil || len(current) != 1 || current[0].Title != target.Title || !current[0].ModifiedAt.Equal(target.ModifiedAt) {
		out.fail("Delete stopped: selected note changed or is no longer accessible", err)
		return out, nil
	}
	if operationID != "" {
		err = b.store.StartNativeNoteAction(ctx, b.config.Source, jobID, operationID, "confirm_delete_note", target.NoteID)
	} else {
		err = b.store.StartNoteAction(ctx, b.config.Source, jobID, "confirm_delete_note", target.NoteID)
	}
	if err != nil {
		out.fail("Delete could not claim its native operation", err)
		return out, err
	}
	err = b.notes.MoveToRecentlyDeleted(ctx, target.NoteID)
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err == nil {
		if markErr := b.store.MarkNoteDeleted(persist, b.config.Source, target.NoteID); markErr != nil {
			err = errors.Join(ErrUncertain, markErr)
		}
	}
	if err == nil {
		out.Message = "Moved to Recently Deleted and verified inactive"
	}
	err = b.finishNoteAction(ctx, jobID, &out, err)
	return out, err
}

func (b *Bridge) finishNoteAction(ctx context.Context, jobID int64, out *noteOutcome, effectErr error) error {
	if effectErr != nil {
		out.fail("Could not verify the Notes operation", effectErr)
	}
	state := "completed"
	if out.IsError {
		state = "failed"
	}
	if out.ReplayUnsafe {
		state = "unknown"
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := b.store.FinishNoteAction(persist, b.config.Source, jobID, state, out.Message); err != nil {
		out.fail("Could not persist Notes outcome", ErrUncertain)
		return errors.Join(ErrUncertain, err)
	}
	if out.ReplayUnsafe {
		return errors.Join(ErrUncertain, effectErr)
	}
	return nil
}
