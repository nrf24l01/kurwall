// Package live streams traffic and node status to dashboard WebSocket clients.
package live

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
)

const (
	ticketTTL    = 30 * time.Second
	ticketSkew   = 5 * time.Second
	writeTimeout = 10 * time.Second
)

// Message is one WebSocket frame.
type Message struct {
	Type       string         `json:"type"`
	NodeID     string         `json:"nodeId,omitempty"`
	Status     string         `json:"status,omitempty"`
	Events     []domain.Event `json:"events,omitempty"`
	ServerTime time.Time      `json:"serverTime"`
}

type client struct {
	conn *websocket.Conn
	send chan []byte
	done chan struct{}
	once sync.Once
}

func (c *client) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

// Hub owns dashboard WebSocket clients and a short-lived ticket signer.
type Hub struct {
	mu      sync.RWMutex
	clients map[*client]struct{}
	secret  []byte
	logger  zerolog.Logger
}

// New builds a live hub.
func New(logger zerolog.Logger) *Hub {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		// The signing secret only protects 30-second dashboard tickets. Refuse
		// to run with a predictable value.
		panic(fmt.Sprintf("generate websocket ticket secret: %v", err))
	}
	return &Hub{clients: map[*client]struct{}{}, secret: secret, logger: logger}
}

// IssueTicket returns a short-lived ticket for the WebSocket handshake.
func (h *Hub) IssueTicket() (string, time.Time) {
	expires := time.Now().UTC().Add(ticketTTL)
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	payload := fmt.Sprintf("%d.%s", expires.Unix(), base64.RawURLEncoding.EncodeToString(nonce))
	return payload + "." + h.sign(payload), expires
}

func (h *Hub) sign(payload string) string {
	mac := hmac.New(sha256.New, h.secret)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ValidateTicket checks expiry and signature.
func (h *Hub) ValidateTicket(ticket string) bool {
	parts := strings.Split(ticket, ".")
	if len(parts) != 3 {
		return false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false
	}
	if time.Now().Unix() > expires+int64(ticketSkew/time.Second) {
		return false
	}
	expected := h.sign(parts[0] + "." + parts[1])
	return hmac.Equal([]byte(expected), []byte(parts[2]))
}

// PublishEvents fans a persisted batch out to all dashboards.
func (h *Hub) PublishEvents(nodeID string, events []domain.Event) {
	if len(events) == 0 || !h.hasClients() {
		return
	}
	payload, err := json.Marshal(Message{
		Type: "events", NodeID: nodeID, Events: events, ServerTime: time.Now().UTC(),
	})
	if err != nil {
		h.logger.Warn().Err(err).Msg("failed to encode live event batch")
		return
	}
	h.broadcast(payload)
}

// PublishNodeStatus informs dashboards about connectivity changes.
func (h *Hub) PublishNodeStatus(nodeID, status string) {
	if !h.hasClients() {
		return
	}
	payload, err := json.Marshal(Message{
		Type: "node", NodeID: nodeID, Status: status, ServerTime: time.Now().UTC(),
	})
	if err != nil {
		return
	}
	h.broadcast(payload)
}

func (h *Hub) hasClients() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients) > 0
}

func (h *Hub) broadcast(payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c.send <- payload:
		default:
			// Live data is best-effort; the durable history remains in ClickHouse.
			h.logger.Debug().Msg("dropping live message for a slow dashboard client")
		}
	}
}

func (h *Hub) register(c *client) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *Hub) unregister(c *client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 8192,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		parsed, err := url.Parse(origin)
		if err != nil {
			return false
		}
		if strings.EqualFold(parsed.Host, r.Host) {
			return true
		}
		// Vite's development proxy changes the Host header; allow the loopback
		// development origins.
		switch parsed.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return true
		}
		return false
	},
}

// Serve upgrades a request and streams messages until the client leaves.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request) {
	if !h.ValidateTicket(r.URL.Query().Get("ticket")) {
		http.Error(w, "invalid or expired stream ticket", http.StatusUnauthorized)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Debug().Err(err).Msg("websocket upgrade failed")
		return
	}
	c := &client{conn: conn, send: make(chan []byte, 256), done: make(chan struct{})}
	h.register(c)
	defer func() {
		h.unregister(c)
		c.close()
	}()

	hello, _ := json.Marshal(Message{Type: "hello", ServerTime: time.Now().UTC()})
	c.send <- hello

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-c.done:
				return
			case payload := <-c.send:
				_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
				if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
					c.close()
					return
				}
			case <-ticker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					c.close()
					return
				}
			}
		}
	}()

	conn.SetReadLimit(4096)
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
	c.close()
	<-writerDone
	_ = conn.Close()
}
