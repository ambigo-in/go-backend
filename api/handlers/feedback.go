package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/ride"
)

type FeedbackHandler struct {
	FeedbackStore *ride.FeedbackStore
	AuthStore     *auth.Store
}

func NewFeedbackHandler(fStore *ride.FeedbackStore, authStore *auth.Store) *FeedbackHandler {
	return &FeedbackHandler{
		FeedbackStore: fStore,
		AuthStore:     authStore,
	}
}

func (h *FeedbackHandler) HandleSubmitFeedback(w http.ResponseWriter, r *http.Request) {
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req ride.Feedback
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	req.UserID = uidStr // Enforce user ID from JWT
	if !response.Validate(w, &req) {
		return
	}

	if err := h.FeedbackStore.InsertFeedback(r.Context(), &req); err != nil {
		response.Error(w, "Error submitting the feedback", http.StatusBadRequest)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"detail": "Feedback submitted successfully"})
}

// adminFeedback is a feedback row plus resolved names so the console never
// shows raw user/driver IDs.
type adminFeedback struct {
	ride.Feedback
	UserName   string `json:"user_name,omitempty"`
	DriverName string `json:"driver_name,omitempty"`
}

// enrichAdminFeedback resolves user and driver names. Each lookup is
// non-fatal: a failure leaves the name empty instead of failing the list.
func (h *FeedbackHandler) enrichAdminFeedback(ctx context.Context, list []ride.Feedback) []adminFeedback {
	out := make([]adminFeedback, 0, len(list))
	for _, f := range list {
		af := adminFeedback{Feedback: f}
		if f.UserID != "" {
			if u, err := h.AuthStore.FindUserByID(ctx, f.UserID); err == nil && u != nil {
				af.UserName = u.Name
			}
		}
		if f.DriverID != "" {
			if drv, err := h.AuthStore.FindDriverByID(ctx, f.DriverID); err == nil && drv != nil {
				af.DriverName = drv.Name
			}
		}
		out = append(out, af)
	}
	return out
}

func (h *FeedbackHandler) HandleAdminListFeedback(w http.ResponseWriter, r *http.Request) {
	list, err := h.FeedbackStore.ListAllFeedback(r.Context())
	if err != nil {
		response.Error(w, "Failed to list feedback", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h.enrichAdminFeedback(r.Context(), list))
}

func (h *FeedbackHandler) HandleListFeedback(w http.ResponseWriter, r *http.Request) {
	// Driver fetching their own feedback
	uidStr, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok {
		response.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	list, err := h.FeedbackStore.ListFeedback(r.Context(), uidStr)
	if err != nil {
		response.Error(w, "Failed to list feedback", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}
