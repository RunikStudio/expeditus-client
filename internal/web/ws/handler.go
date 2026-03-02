package ws

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"ExpeditusClient/internal/web/models"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type Client struct {
	conn      *websocket.Conn
	sessionID string
	send      chan []byte
}

type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	sessions   map[string]*SessionState
	sessionsMu sync.RWMutex
}

type SessionState struct {
	mu         sync.RWMutex
	Progress   float64
	Stage      string
	Status     string
	Processed  int
	TotalItems int
	Speed      string
	ETA        string
}

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		sessions:   make(map[string]*SessionState),
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.clients[client] = true
			log.Printf("Client registered for session: %s", client.sessionID)

		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				log.Printf("Client unregistered for session: %s", client.sessionID)
			}

		case message := <-h.broadcast:
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
		}
	}
}

func (h *Hub) GetOrCreateSession(sessionID string) *SessionState {
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()

	if session, exists := h.sessions[sessionID]; exists {
		return session
	}

	session := &SessionState{
		Stage:    models.StageLogin,
		Status:   models.SessionStatusRunning,
		Progress: 0,
	}
	h.sessions[sessionID] = session
	return session
}

func (h *Hub) UpdateProgress(sessionID string, progress *models.ProgressUpdate) {
	h.sessionsMu.RLock()
	session, exists := h.sessions[sessionID]
	h.sessionsMu.RUnlock()

	if !exists {
		return
	}

	session.mu.Lock()
	session.Progress = progress.Progress
	session.Stage = progress.Stage
	session.Processed = progress.Processed
	session.TotalItems = progress.TotalItems
	session.Speed = progress.Speed
	session.ETA = progress.ETA
	session.mu.Unlock()

	data, _ := json.Marshal(progress)
	h.broadcast <- data
}

func (h *Hub) SendToSession(sessionID string, data []byte) {
	for client := range h.clients {
		if client.sessionID == sessionID {
			select {
			case client.send <- data:
			default:
			}
		}
	}
}

func (h *Hub) GetSession(sessionID string) *SessionState {
	h.sessionsMu.RLock()
	defer h.sessionsMu.RUnlock()
	return h.sessions[sessionID]
}

func (h *Hub) Register(client *Client) {
	h.register <- client
}

func (h *Hub) Unregister(client *Client) {
	h.unregister <- client
}

var globalHub = NewHub()

func Init() {
	go globalHub.Run()
}

func HandleWebSocket(c *gin.Context) {
	sessionID := c.Query("sessionId")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sessionId required"})
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	client := &Client{
		conn:      conn,
		sessionID: sessionID,
		send:      make(chan []byte, 256),
	}

	globalHub.Register(client)
	defer globalHub.Unregister(client)

	go client.writePump()
	client.readPump()
}

func (c *Client) readPump() {
	defer func() {
		globalHub.Unregister(c)
		c.conn.Close()
	}()

	c.conn.SetReadLimit(512 * 1024)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}

		var progress models.ProgressUpdate
		if err := json.Unmarshal(message, &progress); err == nil {
			globalHub.UpdateProgress(c.sessionID, &progress)
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func UpdateProgress(sessionID string, progress *models.ProgressUpdate) {
	globalHub.UpdateProgress(sessionID, progress)
}

func SendToSession(sessionID string, data interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return
	}
	globalHub.SendToSession(sessionID, jsonData)
}
