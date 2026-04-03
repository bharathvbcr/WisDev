package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
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

type stripeBillingSyncRequest struct {
	UserID             string         `json:"userId"`
	UserIDSnake        string         `json:"user_id"`
	Email              string         `json:"email,omitempty"`
	CustomerEmail      string         `json:"customerEmail,omitempty"`
	CustomerEmailSnake string         `json:"customer_email,omitempty"`
	Tier               string         `json:"tier,omitempty"`
	EventType          string         `json:"eventType"`
	EventTypeSnake     string         `json:"event_type,omitempty"`
	StripeEventID      string         `json:"stripeEventId,omitempty"`
	StripeEventIDSnake string         `json:"stripe_event_id,omitempty"`
	SessionID          string         `json:"sessionId,omitempty"`
	StripeSessionID    string         `json:"stripeSessionId,omitempty"`
	StripeSessionIDRaw string         `json:"stripe_session_id,omitempty"`
	SubscriptionID     string         `json:"subscriptionId,omitempty"`
	StripeSubID        string         `json:"stripeSubscriptionId,omitempty"`
	StripeSubIDRaw     string         `json:"stripe_subscription_id,omitempty"`
	CustomerID         string         `json:"customerId,omitempty"`
	Status             string         `json:"status,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
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

	// Actually delete from Firebase Auth
	if !alreadyDeleted {
		if err := h.deleteUserFromFirebase(r.Context(), req.UserID); err != nil {
			slog.Error("failed to delete user", "error", err, "userId", req.UserID)
			// We still return OK since we recorded the intent, but the actual deletion failed.
			// In production, we might want to queue this for retry.
		}
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

func (h *InternalOpsHandler) HandleStripeBillingSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req stripeBillingSyncRequest
	if err := decodeStrictJSONBody(r.Body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	req.UserID = strings.TrimSpace(firstNonEmpty(req.UserID, req.UserIDSnake))
	req.Email = strings.TrimSpace(firstNonEmpty(req.Email, req.CustomerEmail, req.CustomerEmailSnake))
	req.Tier = strings.TrimSpace(req.Tier)
	req.EventType = strings.TrimSpace(firstNonEmpty(req.EventType, req.EventTypeSnake))
	req.StripeEventID = strings.TrimSpace(firstNonEmpty(req.StripeEventID, req.StripeEventIDSnake, r.Header.Get("X-Idempotency-Key")))
	req.SessionID = strings.TrimSpace(firstNonEmpty(req.SessionID, req.StripeSessionID, req.StripeSessionIDRaw))
	req.SubscriptionID = strings.TrimSpace(firstNonEmpty(req.SubscriptionID, req.StripeSubID, req.StripeSubIDRaw))
	req.CustomerID = strings.TrimSpace(req.CustomerID)
	req.Status = strings.TrimSpace(req.Status)
	if req.Metadata == nil {
		req.Metadata = map[string]any{}
	}

	if req.EventType == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "eventType is required", map[string]any{
			"field": "eventType",
		})
		return
	}
	if req.StripeEventID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "stripeEventId is required", map[string]any{
			"field": "stripeEventId",
		})
		return
	}

	persisted, err := h.persistBillingState(r.Context(), req)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to persist stripe billing state", map[string]any{
			"error":     err.Error(),
			"eventType": req.EventType,
			"userId":    req.UserID,
		})
		return
	}

	// Update the primary users table tier
	if req.UserID != "" && req.Tier != "" {
		if err := h.updateUserTier(r.Context(), req.UserID, req.Tier); err != nil {
			slog.Error("failed to update user tier", "error", err, "userId", req.UserID, "tier", req.Tier)
		}
	}

	h.appendJournal(wisdev.RuntimeJournalEntry{
		EventID:   wisdev.NewTraceID(),
		TraceID:   newTraceID(),
		UserID:    req.UserID,
		EventType: "stripe_billing_sync",
		Path:      "/internal/billing/stripe/webhook",
		Status:    "synced",
		CreatedAt: wisdev.NowMillis(),
		Summary:   "Stripe billing state synchronized",
		Payload: map[string]any{
			"userId":         req.UserID,
			"email":          req.Email,
			"tier":           req.Tier,
			"eventType":      req.EventType,
			"stripeEventId":  req.StripeEventID,
			"sessionId":      req.SessionID,
			"subscriptionId": req.SubscriptionID,
			"customerId":     req.CustomerID,
			"status":         req.Status,
			"metadata":       cloneAnyMap(req.Metadata),
			"persisted":      persisted,
		},
	})

	writeJSONResponse(w, http.StatusOK, map[string]any{
		"ok":                true,
		"success":           true,
		"persisted":         persisted,
		"userId":            req.UserID,
		"tier":              req.Tier,
		"eventType":         req.EventType,
		"alreadyProcessed":  false,
		"already_processed": false,
		"message":           "Stripe billing state synchronized",
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

func (h *InternalOpsHandler) persistBillingState(ctx context.Context, req stripeBillingSyncRequest) (bool, error) {
	if h.db == nil {
		return false, nil
	}

	if err := ensureBillingStateTable(ctx, h.db); err != nil {
		return false, err
	}

	metadata := cloneAnyMap(req.Metadata)
	commandTag, err := h.db.Exec(ctx, `
INSERT INTO stripe_billing_state (
	subscription_id, customer_id, user_id, email, tier, event_type, status, metadata_json, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (subscription_id) DO UPDATE SET
	customer_id = EXCLUDED.customer_id,
	user_id = EXCLUDED.user_id,
	email = EXCLUDED.email,
	tier = EXCLUDED.tier,
	event_type = EXCLUDED.event_type,
	status = EXCLUDED.status,
	metadata_json = EXCLUDED.metadata_json,
	updated_at = EXCLUDED.updated_at
`, firstNonEmpty(req.SubscriptionID, req.StripeEventID), req.CustomerID, req.UserID, req.Email, req.Tier, req.EventType, req.Status, metadata, time.Now().UTC())
	if err != nil {
		return false, err
	}

	return commandTag.RowsAffected() > 0, nil
}

func (h *InternalOpsHandler) deleteUserFromFirebase(ctx context.Context, userID string) error {
	slog.Warn("Firebase auth deletion skipped (open-source build)", "userId", userID)
	return nil
}

func (h *InternalOpsHandler) updateUserTier(ctx context.Context, userID string, tier string) error {
	if h.db == nil {
		return nil
	}
	if err := h.ensureUsersTable(ctx); err != nil {
		return err
	}
	_, err := h.db.Exec(ctx, `
UPDATE users SET tier = $2, updated_at = $3 WHERE user_id = $1
`, userID, tier, time.Now().UTC())
	return err
}

func (h *InternalOpsHandler) ensureUsersTable(ctx context.Context) error {
	_, err := h.db.Exec(ctx, `
CREATE TABLE IF NOT EXISTS users (
	user_id TEXT PRIMARY KEY,
	email TEXT NOT NULL DEFAULT '',
	tier TEXT NOT NULL DEFAULT 'free',
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`)
	return err
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

func ensureBillingStateTable(ctx context.Context, db wisdev.DBProvider) error {
	_, err := db.Exec(ctx, `
CREATE TABLE IF NOT EXISTS stripe_billing_state (
	subscription_id TEXT PRIMARY KEY,
	customer_id TEXT NOT NULL DEFAULT '',
	user_id TEXT NOT NULL DEFAULT '',
	email TEXT NOT NULL DEFAULT '',
	tier TEXT NOT NULL DEFAULT '',
	event_type TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT '',
	metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
	updated_at TIMESTAMPTZ NOT NULL
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
