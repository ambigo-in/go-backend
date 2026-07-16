package handlers

import (
	"encoding/json"
	"net/http"

	"ambigo-backend/api/middleware"
	"ambigo-backend/api/response"
	"ambigo-backend/internal/ride"
)

type FeedbackHandler struct {
	FeedbackStore *ride.FeedbackStore
}

func NewFeedbackHandler(fStore *ride.FeedbackStore) *FeedbackHandler {
	return &FeedbackHandler{
		FeedbackStore: fStore,
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

func (h *FeedbackHandler) HandleAdminListFeedback(w http.ResponseWriter, r *http.Request) {
	list, err := h.FeedbackStore.ListAllFeedback(r.Context())
	if err != nil {
		response.Error(w, "Failed to list feedback", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
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
