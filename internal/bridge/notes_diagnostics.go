package bridge

import (
	"context"
	"errors"
	"os"

	"github.com/teslashibe/notes"
)

// Only fixed connector errors may reach logs or the model. Native errors can
// contain note text, paths or script output; never copy arbitrary error strings.
func noteFailureReason(err error) (string, string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", "Notes automation timed out"
	case errors.Is(err, context.Canceled):
		return "canceled", "Notes automation was canceled"
	case errors.Is(err, notes.ErrUnsupported):
		return "unsupported", "The native Notes helper is not configured or supported"
	case errors.Is(err, os.ErrNotExist):
		return "executable_missing", "A required Notes automation executable could not be found"
	}
	var op *notes.OperationError
	if errors.As(err, &op) && op.Err != nil {
		switch op.Err.Error() {
		case "Accessibility permission required":
			return "accessibility_required", "The native Notes helper requires macOS Accessibility permission"
		case "Notes automation busy":
			return "busy", "Another Notes automation operation is in progress"
		case "Notes has a modal dialog; finish it manually", "existing Notes modal; refusing to take ownership":
			return "modal_open", "An open Notes dialog needs to be finished manually"
		case "ambiguous note editor", "note editor identity or content changed":
			return "editor_unverified", "The native helper could not verify the selected Notes editor"
		case "Notes editor unavailable while the Mac is locked":
			return "desktop_locked", "Unlock the Mac before changing native Notes checklists"
		case "native checklist state unavailable", "unknown native checkbox state", "missing editor text":
			return "checklist_unavailable", "The native helper could not read checklist state from the Notes editor"
		case "Notes lost foreground; keyboard automation stopped", "Notes is not foreground; native UI automation stopped":
			return "foreground_lost", "Notes lost focus; native automation stopped"
		}
	}
	return "unclassified", ""
}
