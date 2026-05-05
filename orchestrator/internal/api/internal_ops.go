package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

type InternalOpsHandler struct {
	db      wisdev.DBProvider
	journal *wisdev.RuntimeJournal
}

func NewInternalOpsHandler(db wisdev.DBProvider, journal *wisdev.RuntimeJournal) *InternalOpsHandler {
	return &InternalOpsHandler{
		db:      db,
		journal: journal,
	}
}

type accountDeleteRequest struct {
	UserID         string `json:"userId"`
	UserIDSnake    string `json:"user_id"`
	Email          string `json:"email,omitempty"`
	Reason         string `json:"reason,omitempty"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	IdempotencyRaw string `json:"idempotency_key,omitempty"`
}

func (h *InternalOpsHandler) HandleAccountDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req accountDeleteRequest
	if err := decodeStrictJSONBody(r.Body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	req.UserID = strings.TrimSpace(firstNonEmpty(req.UserID, req.UserIDSnake))
	req.Email = strings.TrimSpace(req.Email)
	req.Reason = strings.TrimSpace(req.Reason)
	req.IdempotencyKey = strings.TrimSpace(firstNonEmpty(req.IdempotencyKey, req.IdempotencyRaw, r.Header.Get("X-Idempotency-Key")))
	if req.UserID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "userId is required", map[string]any{
			"field": "userId",
		})
		return
	}
	if req.Reason == "" {
		req.Reason = "user_requested"
	}

	alreadyDeleted, persisted, err := h.persistAccountDeletion(r.Context(), req)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to persist account deletion", map[string]any{
			"error":  err.Error(),
			"userId": req.UserID,
		})
		return
	}

	h.appendJournal(wisdev.RuntimeJournalEntry{
		EventID:   wisdev.NewTraceID(),
		TraceID:   newTraceID(),
		UserID:    req.UserID,
		EventType: "account_delete",
		Path:      "/internal/account/delete",
		Status:    statusLabel(alreadyDeleted),
		CreatedAt: wisdev.NowMillis(),
		Summary:   "Account deletion recorded",
		Payload: map[string]any{
			"userId":         req.UserID,
			"email":          req.Email,
			"reason":         req.Reason,
			"idempotencyKey": req.IdempotencyKey,
			"alreadyDeleted": alreadyDeleted,
			"persisted":      persisted,
		},
	})

	writeJSONResponse(w, http.StatusOK, map[string]any{
		"ok":                true,
		"success":           true,
		"userId":            req.UserID,
		"alreadyDeleted":    alreadyDeleted,
		"alreadyProcessed":  alreadyDeleted,
		"already_processed": alreadyDeleted,
		"persisted":         persisted,
		"status":            statusLabel(alreadyDeleted),
		"message":           accountDeleteMessage(req.UserID, alreadyDeleted),
	})
}

func (h *InternalOpsHandler) persistAccountDeletion(ctx context.Context, req accountDeleteRequest) (bool, bool, error) {
	if h.db == nil {
		return false, false, nil
	}

	if err := ensureAccountDeletionTable(ctx, h.db); err != nil {
		return false, false, err
	}

	commandTag, err := h.db.Exec(ctx, `
INSERT INTO account_deletion_requests (
	user_id, email, reason, requested_at, updated_at, deleted
) VALUES ($1, $2, $3, $4, $4, TRUE)
ON CONFLICT (user_id) DO UPDATE SET
	email = EXCLUDED.email,
	reason = EXCLUDED.reason,
	updated_at = EXCLUDED.updated_at,
	deleted = TRUE
WHERE account_deletion_requests.deleted IS DISTINCT FROM TRUE
`, req.UserID, req.Email, req.Reason, time.Now().UTC())
	if err != nil {
		return false, false, err
	}

	rowsAffected := commandTag.RowsAffected()
	return rowsAffected == 0, true, nil
}

func ensureAccountDeletionTable(ctx context.Context, db wisdev.DBProvider) error {
	_, err := db.Exec(ctx, `
CREATE TABLE IF NOT EXISTS account_deletion_requests (
	user_id TEXT PRIMARY KEY,
	email TEXT NOT NULL DEFAULT '',
	reason TEXT NOT NULL DEFAULT '',
	requested_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL,
	deleted BOOLEAN NOT NULL DEFAULT FALSE
);
`)
	return err
}

func (h *InternalOpsHandler) appendJournal(entry wisdev.RuntimeJournalEntry) {
	if h == nil || h.journal == nil {
		return
	}
	h.journal.Append(entry)
}

func statusLabel(alreadyDeleted bool) string {
	if alreadyDeleted {
		return "already_deleted"
	}
	return "deletion_recorded"
}

func accountDeleteMessage(userID string, alreadyDeleted bool) string {
	if alreadyDeleted {
		return "Account deletion already processed for " + userID
	}
	return "Account deletion recorded for " + userID
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
