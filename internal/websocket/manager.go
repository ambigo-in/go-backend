package websocket

import (
	"context"
	"ambigo-backend/internal/auth"
	"ambigo-backend/internal/eventbus"
	"ambigo-backend/internal/location"
	"ambigo-backend/internal/metrics"
	"encoding/json"
	"log"
	"sync"
)

// DeclineHandler is implemented by the dispatcher to handle driver ride declines.
type DeclineHandler interface {
	HandleDriverDecline(ctx context.Context, rideID, driverID string)
}

// Manager maintains the set of active clients and broadcasts messages to the
// clients.
type Manager struct {
	// Registered clients.
	// clients maps Role -> ID -> set of *Client
	clients map[string]map[string]map[*Client]bool

	// Ride watchers are clients currently viewing a ride screen.
	rideWatchers map[string]map[*Client]bool

	// Active ride lookup lets driver location updates reach the watching user.
	activeDriverRide map[string]string

	// Inbound messages from the clients.
	broadcast chan []byte

	// Register requests from the clients.
	register chan *Client

	// Unregister requests from clients.
	unregister chan *Client

	mu sync.RWMutex

	// Location Store for updating driver GPS
	LocStore *location.MemoryStore

	// DeclineHandler receives RIDE_DECLINED events from clients
	DeclineHandler DeclineHandler

	// AuthStore for looking up driver details (vehicle type, etc.)
	AuthStore *auth.Store

	// EventBus for publishing driver location updates
	EventBus *eventbus.InMemoryBus
}

func NewManager(locStore *location.MemoryStore, authStore *auth.Store, eventBus *eventbus.InMemoryBus) *Manager {
	return &Manager{
		broadcast:        make(chan []byte),
		register:         make(chan *Client),
		unregister:       make(chan *Client),
		clients:          make(map[string]map[string]map[*Client]bool),
		rideWatchers:     make(map[string]map[*Client]bool),
		activeDriverRide: make(map[string]string),
		LocStore:         locStore,
		AuthStore:        authStore,
		EventBus:         eventBus,
	}
}

func (m *Manager) Run() {
	m.clients["driver"] = make(map[string]map[*Client]bool)
	m.clients["user"] = make(map[string]map[*Client]bool)

	for {
		select {
		case client := <-m.register:
			m.mu.Lock()
			if m.clients[client.Role][client.ID] == nil {
				m.clients[client.Role][client.ID] = make(map[*Client]bool)
			}
			m.clients[client.Role][client.ID][client] = true
			m.mu.Unlock()
			metrics.ActiveConnections.Inc()
			log.Printf("[WebSocket] %s registered: %s", client.Role, client.ID)

		case client := <-m.unregister:
			m.mu.Lock()
			if clientsForID, ok := m.clients[client.Role][client.ID]; ok {
				if _, exists := clientsForID[client]; exists {
					delete(clientsForID, client)
					close(client.Send)
					metrics.ActiveConnections.Dec()
					log.Printf("[WebSocket] %s unregistered: %s", client.Role, client.ID)
				}
				if len(clientsForID) == 0 {
					delete(m.clients[client.Role], client.ID)
				}
			}
			for rideID, watchers := range m.rideWatchers {
				delete(watchers, client)
				if len(watchers) == 0 {
					delete(m.rideWatchers, rideID)
				}
			}
			m.mu.Unlock()

		case message := <-m.broadcast:
			// Example generic broadcast (rarely used, usually we target specific IDs)
			m.mu.RLock()
			for _, roleClients := range m.clients {
				for _, clientsForID := range roleClients {
					for client := range clientsForID {
						select {
						case client.Send <- message:
						default:
							// Ignore if send buffer is full
						}
					}
				}
			}
			m.mu.RUnlock()
		}
	}
}

// RegisterClient adds a new client to the hub
func (m *Manager) RegisterClient(client *Client) {
	m.register <- client
}

// UnregisterClient removes a client from the hub
func (m *Manager) UnregisterClient(client *Client) {
	m.unregister <- client
}

// SendToClient sends a specific JSON message to a specific user or driver
func (m *Manager) SendToClient(role, id string, msgType string, payload interface{}) {
	m.mu.RLock()
	clientsForID, ok := m.clients[role][id]
	m.mu.RUnlock()

	if !ok || len(clientsForID) == 0 {
		return
	}

	rawPayload, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[WebSocket] failed to marshal payload for %s %s: %v", role, id, err)
		return
	}

	baseMsg := BaseMessage{
		Type:    msgType,
		Payload: rawPayload,
	}

	finalMsg, err := json.Marshal(baseMsg)
	if err != nil {
		log.Printf("[WebSocket] failed to marshal baseMsg for %s %s: %v", role, id, err)
		return
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	// Re-check in case it changed
	clientsForID, ok = m.clients[role][id]
	if !ok {
		return
	}

	for client := range clientsForID {
		select {
		case client.Send <- finalMsg:
			log.Printf("[WebSocket] Queued %s to %s %s (chan %d/%d)", msgType, role, id, len(client.Send), cap(client.Send))
		default:
			log.Printf("[WebSocket] WARNING: Send channel full for %s %s, dropping %s message!", role, id, msgType)
		}
	}
}

func (m *Manager) SendToRideWatchers(rideID string, msgType string, payload interface{}) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return
	}
	msgBytes, err := json.Marshal(BaseMessage{Type: msgType, Payload: payloadBytes})
	if err != nil {
		return
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	for client := range m.rideWatchers[rideID] {
		select {
		case client.Send <- msgBytes:
		default:
		}
	}
}

func (m *Manager) SetActiveRide(driverID, rideID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeDriverRide[driverID] = rideID
}

func (m *Manager) ClearActiveRide(driverID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.activeDriverRide, driverID)
}

// HandleIncomingMessage parses messages sent from a client to the server
func (m *Manager) HandleIncomingMessage(client *Client, message []byte) {
	// Support raw text ping from frontend without JSON parsing errors
	if string(message) == "ping" {
		return
	}

	var baseMsg BaseMessage
	if err := json.Unmarshal(message, &baseMsg); err != nil {
		log.Printf("[WebSocket] Error parsing message from %s %s: %v", client.Role, client.ID, err)
		return
	}

	switch baseMsg.Type {
	case EventLocationUpdate:
		m.handleLocationUpdate(client, baseMsg.Payload)
	case EventWatchRide:
		var payload struct {
			RideID string `json:"ride_id"`
		}
		if err := json.Unmarshal(baseMsg.Payload, &payload); err != nil || payload.RideID == "" {
			log.Printf("[WebSocket] Invalid WATCH_RIDE from %s %s", client.Role, client.ID)
			return
		}
		m.mu.Lock()
		if m.rideWatchers[payload.RideID] == nil {
			m.rideWatchers[payload.RideID] = make(map[*Client]bool)
		}
		m.rideWatchers[payload.RideID][client] = true
		m.mu.Unlock()
	case EventRideDeclined:
		var payload struct {
			RideID string `json:"ride_id"`
		}
		if err := json.Unmarshal(baseMsg.Payload, &payload); err != nil || payload.RideID == "" {
			log.Printf("[WebSocket] Invalid RIDE_DECLINED from %s %s", client.Role, client.ID)
			return
		}
		if m.DeclineHandler != nil {
			m.DeclineHandler.HandleDriverDecline(context.Background(), payload.RideID, client.ID)
		}
	case "PING":
		// Ignore ping messages
	default:
		log.Printf("[WebSocket] Unknown event type '%s' from %s %s", baseMsg.Type, client.Role, client.ID)
	}
}
