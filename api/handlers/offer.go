package handlers

import (
	"encoding/json"
	"net/http"

	"ambigo-backend/api/response"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/offer"
	"ambigo-backend/internal/requestid"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type OfferHandler struct {
	Store    *offer.Store
	EventBus *eventbus.InMemoryBus
}

func NewOfferHandler(store *offer.Store, eventBus *eventbus.InMemoryBus) *OfferHandler {
	return &OfferHandler{Store: store, EventBus: eventBus}
}

func (h *OfferHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())

	var o offer.Offer
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
		response.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}
	if !response.Validate(w, &o) {
		return
	}

	if err := h.Store.Create(r.Context(), &o); err != nil {
		response.Error(w, "Failed to create offer", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminOfferCreated, eventbus.AdminOfferPayload{
		OfferID: o.ID.Hex(), Description: o.Description, RequestID: reqID,
	})

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"detail": "Created",
		"id":     o.ID.Hex(),
	})
}

func (h *OfferHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	list, err := h.Store.List(r.Context())
	if err != nil {
		response.Error(w, "Failed to fetch offers", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func (h *OfferHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	reqID := requestid.FromContext(r.Context())
	idStr := r.PathValue("id")
	objID, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		response.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if err := h.Store.Delete(r.Context(), objID); err != nil {
		response.Error(w, "Failed to delete offer", http.StatusInternalServerError)
		return
	}

	h.EventBus.PublishEvent(eventbus.ChannelAdminOfferDeleted, eventbus.AdminOfferPayload{
		OfferID: idStr, RequestID: reqID,
	})

	json.NewEncoder(w).Encode(map[string]string{"detail": "Deleted"})
}
