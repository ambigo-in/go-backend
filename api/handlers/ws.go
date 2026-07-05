package handlers

import (
	"net/http"

	"ambigo-backend/api/response"
	"ambigo-backend/config"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/websocket"

	gorilla "github.com/gorilla/websocket"
)

var upgrader = gorilla.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Allow all origins for mobile apps
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// ServeWS handles WebSocket requests from the peer.
func ServeWS(manager *websocket.Manager, cfg *config.AppConfig, w http.ResponseWriter, r *http.Request) {
	log := logger.Ctx(r.Context())

	// 0. Validate API Key
	apiKey := r.URL.Query().Get("api_key")
	if apiKey == "" || apiKey != cfg.APIKey {
		response.Error(w, "Unauthorized: invalid API key", http.StatusUnauthorized)
		return
	}

	// 1. Extract Token from Query Parameter (e.g. ws://domain/ws?token=XYZ)
	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" {
		response.Error(w, "Unauthorized: missing token", http.StatusUnauthorized)
		return
	}

	// 2. Validate JWT Token
	claims, err := auth.ValidateToken(tokenStr, cfg.JWTSecret)
	if err != nil {
		log.Error().Err(err).Msg("WebSocket auth failed")
		response.Error(w, "Unauthorized: invalid token", http.StatusUnauthorized)
		return
	}

	// 3. Upgrade HTTP connection to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("WebSocket upgrade failed")
		return
	}

	// 4. Create and Register Client
	client := &websocket.Client{
		Manager: manager,
		Conn:    conn,
		Send:    make(chan []byte, 256),
		ID:      claims.ID,
		Role:    claims.Role,
	}

	// Wait for the manager to register the client
	manager.RegisterClient(client)

	// 5. Start read and write pumps in goroutines
	go client.WritePump()
	go client.ReadPump()
}
