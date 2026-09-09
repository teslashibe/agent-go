package bridge

import (
	"context"
	"net/http"

	"github.com/teslashibe/codex"
)

// ApprovalRunner is the reviewed app-server path. YOLO supplies a nil handler;
// leftover Codex elicitations are accepted inside the harness, not over iMessage.
type ApprovalRunner interface {
	RunWithApprovals(context.Context, string, string, codex.ApprovalHandler, http.Handler) (Result, error)
}
