package websocket

import (
	"time"

	"ambigo-backend/internal/logger"

	"github.com/gorilla/websocket"
)

const (
	writeWait = 10 * time.Second

	pongWait = 60 * time.Second

	pingPeriod = (pongWait * 9) / 10

	maxMessageSize = 1024
)

// Client is a middleman between the websocket connection and the hub.
type Client struct {
	Manager *Manager

	Conn *websocket.Conn

	Send chan []byte

	ID string

	Role string
}

// ReadPump pumps messages from the websocket connection to the hub.
func (c *Client) ReadPump() {
	defer func() {
		c.Manager.UnregisterClient(c)
		c.Conn.Close()
	}()
	c.Conn.SetReadLimit(maxMessageSize)
	c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error { c.Conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })

	for {
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logger.Log.Error().Err(err).Msg("WebSocket read error")
			}
			break
		}
		c.Manager.HandleIncomingMessage(c, message)
	}
}

// WritePump pumps messages from the hub to the websocket connection.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			logger.Log.Debug().Str("role", c.Role).Str("id", c.ID).Str("msg", string(message)).Msg("Sending to client")
			if err := c.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				logger.Log.Error().Err(err).Str("role", c.Role).Str("id", c.ID).Msg("Write error")
				return
			}

			n := len(c.Send)
			for i := 0; i < n; i++ {
				nextMsg := <-c.Send
				logger.Log.Debug().Str("role", c.Role).Str("id", c.ID).Str("msg", string(nextMsg)).Msg("Sending queued to client")
				if err := c.Conn.WriteMessage(websocket.TextMessage, nextMsg); err != nil {
					logger.Log.Error().Err(err).Str("role", c.Role).Str("id", c.ID).Msg("Write error")
					return
				}
			}
		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
