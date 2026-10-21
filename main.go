package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const maxMessageBytes = 32 * 1024

type envelope struct {
	Version int             `json:"version"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type client struct {
	conn          *websocket.Conn
	writeMu       sync.Mutex
	roomCode      string
	participantID string
}

func newClient(conn *websocket.Conn) *client {
	return &client{conn: conn}
}

func (c *client) send(messageType string, payload interface{}) error {
	if c == nil || c.conn == nil {
		return nil
	}
	b, err := json.Marshal(envelope{Version: protocolVersion, Type: messageType, Payload: mustJSON(payload)})
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return c.conn.WriteMessage(websocket.TextMessage, b)
}

func mustJSON(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (c *client) closeQuietly() {
	if c == nil || c.conn == nil {
		return
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "reconnected elsewhere"), time.Now().Add(time.Second))
	_ = c.conn.Close()
}

type server struct {
	rooms          *RoomManager
	allowedOrigins map[string]bool
	upgrader       websocket.Upgrader
}

func main() {
	s := newServer()
	go s.cleanupLoop()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := "0.0.0.0:" + port
	log.Printf("Track Foundry multiplayer listening on %s", addr)
	if err := http.ListenAndServe(addr, s.routes()); err != nil {
		log.Fatal(err)
	}
}

func newServer() *server {
	s := &server{rooms: NewRoomManager(), allowedOrigins: configuredOrigins()}
	s.upgrader = websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096, CheckOrigin: s.checkOrigin}
	return s
}

func configuredOrigins() map[string]bool {
	origins := map[string]bool{
		"http://localhost:3000": true, "http://localhost:5173": true, "http://localhost:8080": true,
		"http://127.0.0.1:3000": true, "http://127.0.0.1:5173": true, "http://127.0.0.1:8080": true,
		"https://sites.google.com": true,
	}
	for _, origin := range strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",") {
		origin = strings.TrimRight(strings.TrimSpace(origin), "/")
		if origin != "" {
			origins[origin] = true
		}
	}
	return origins
}

func (s *server) checkOrigin(r *http.Request) bool {
	origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
	// A missing Origin is useful for local CLI diagnostics. Browser requests
	// always carry one and must match the explicit allowlist.
	return origin == "" || s.allowedOrigins[origin]
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/v1/rooms", s.roomsHTTP)
	mux.HandleFunc("/v1/rooms/", s.roomHTTP)
	mux.HandleFunc("/ws", s.websocket)
	return s.cors(mux)
}

func (s *server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
		if origin != "" && s.allowedOrigins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "version": protocolVersion, "time": time.Now().UnixMilli()})
}

type createRequest struct {
	Name   string       `json:"name"`
	Config SharedConfig `json:"config"`
}
type joinRequest struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}

func (s *server) roomsHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var request createRequest
	if err := decodeBody(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, err := s.rooms.Create(request.Name, request.Config, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, session)
}

func (s *server) roomHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/rooms/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, errors.New("room code required"))
		return
	}
	code := parts[0]
	if len(parts) == 2 && parts[1] == "join" && r.Method == http.MethodPost {
		var request joinRequest
		if err := decodeBody(r, &request); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		session, err := s.rooms.Join(code, request.Name, request.Token, nil)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, session)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		snapshot, err := s.rooms.AuthorizedSnapshot(code, bearer(r))
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	writeError(w, http.StatusMethodNotAllowed, errors.New("unsupported room operation"))
}

func decodeBody(r *http.Request, target interface{}) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxMessageBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid request payload")
	}
	return nil
}
func bearer(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
}

func (s *server) websocket(w http.ResponseWriter, r *http.Request) {
	if !s.checkOrigin(r) {
		writeError(w, http.StatusForbidden, errors.New("origin is not allowed"))
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := newClient(conn)
	defer func() {
		if c.roomCode != "" {
			s.disconnect(c)
		}
		c.closeQuietly()
	}()
	conn.SetReadLimit(maxMessageBytes)
	_ = conn.SetReadDeadline(time.Now().Add(70 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(70 * time.Second)) })
	go s.keepAlive(c)
	for {
		_, body, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var message envelope
		if err := json.Unmarshal(body, &message); err != nil || message.Version != protocolVersion || len(message.Type) == 0 {
			_ = c.send("error", map[string]string{"message": "Invalid protocol message."})
			continue
		}
		s.handleMessage(c, message)
	}
}

func (s *server) keepAlive(c *client) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if err := c.send("ping", map[string]int64{"serverTime": time.Now().UnixMilli()}); err != nil {
			return
		}
	}
}

func (s *server) handleMessage(c *client, message envelope) {
	err := s.handleMessageInner(c, message)
	if err != nil {
		_ = c.send("error", map[string]string{"message": err.Error()})
	}
}

func (s *server) handleMessageInner(c *client, message envelope) error {
	switch message.Type {
	case "create":
		var p createRequest
		if err := json.Unmarshal(message.Payload, &p); err != nil {
			return errors.New("invalid create request")
		}
		session, err := s.rooms.Create(p.Name, p.Config, c)
		if err != nil {
			return err
		}
		s.sendSession(c, session)
		s.broadcastRoom(session.RoomCode)
		return nil
	case "join":
		var p struct {
			RoomCode string `json:"roomCode"`
			Name     string `json:"name"`
			Token    string `json:"token"`
		}
		if err := json.Unmarshal(message.Payload, &p); err != nil {
			return errors.New("invalid join request")
		}
		session, err := s.rooms.Join(p.RoomCode, p.Name, p.Token, c)
		if err != nil {
			return err
		}
		s.sendSession(c, session)
		s.broadcastRoom(session.RoomCode)
		return nil
	case "auth":
		var p struct {
			RoomCode string `json:"roomCode"`
			Token    string `json:"token"`
		}
		if err := json.Unmarshal(message.Payload, &p); err != nil {
			return errors.New("invalid authentication request")
		}
		session, err := s.rooms.Authenticate(p.RoomCode, p.Token, c)
		if err != nil {
			return err
		}
		s.sendSession(c, session)
		s.broadcastRoom(session.RoomCode)
		return nil
	case "ready":
		if err := s.requireMembership(c); err != nil {
			return err
		}
		var p struct {
			Ready bool `json:"ready"`
		}
		if err := json.Unmarshal(message.Payload, &p); err != nil {
			return errors.New("invalid ready request")
		}
		if err := s.rooms.SetReady(c.roomCode, c.participantID, p.Ready); err != nil {
			return err
		}
		s.broadcastRoom(c.roomCode)
		return nil
	case "config":
		if err := s.requireMembership(c); err != nil {
			return err
		}
		var p struct {
			Config SharedConfig `json:"config"`
		}
		if err := json.Unmarshal(message.Payload, &p); err != nil {
			return errors.New("invalid configuration")
		}
		if err := s.rooms.UpdateConfig(c.roomCode, c.participantID, p.Config); err != nil {
			return err
		}
		s.broadcastRoom(c.roomCode)
		return nil
	case "start":
		if err := s.requireMembership(c); err != nil {
			return err
		}
		startAt, err := s.rooms.Start(c.roomCode, c.participantID)
		if err != nil {
			return err
		}
		clients, _, _ := s.rooms.BroadcastTargets(c.roomCode, "")
		for _, other := range clients {
			_ = other.send("race_start", map[string]int64{"startAt": startAt, "serverTime": time.Now().UnixMilli()})
		}
		s.broadcastRoom(c.roomCode)
		return nil
	case "state":
		if err := s.requireMembership(c); err != nil {
			return err
		}
		var p VehicleState
		if err := json.Unmarshal(message.Payload, &p); err != nil {
			return errors.New("invalid vehicle state")
		}
		clients, err := s.rooms.SetVehicleState(c.roomCode, c.participantID, p)
		if err != nil {
			return err
		}
		payload := map[string]interface{}{"playerId": c.participantID, "state": p, "serverTime": time.Now().UnixMilli()}
		for _, other := range clients {
			_ = other.send("state", payload)
		}
		return nil
	case "finish":
		if err := s.requireMembership(c); err != nil {
			return err
		}
		var p FinishState
		if err := json.Unmarshal(message.Payload, &p); err != nil {
			return errors.New("invalid finish state")
		}
		if err := s.rooms.Finish(c.roomCode, c.participantID, p); err != nil {
			return err
		}
		s.broadcastRoom(c.roomCode)
		return nil
	case "leave":
		if err := s.requireMembership(c); err != nil {
			return err
		}
		s.leave(c, true)
		return nil
	case "ping":
		return c.send("pong", map[string]int64{"serverTime": time.Now().UnixMilli()})
	default:
		return errors.New("unsupported message type")
	}
}

func (s *server) requireMembership(c *client) error {
	if c.roomCode == "" || c.participantID == "" {
		return errors.New("join a room first")
	}
	return nil
}
func (s *server) sendSession(c *client, session Session) { _ = c.send("session", session) }
func (s *server) broadcastRoom(code string) {
	clients, snapshot, err := s.rooms.BroadcastTargets(code, "")
	if err != nil {
		return
	}
	for _, c := range clients {
		_ = c.send("room", snapshot)
	}
}
func (s *server) disconnect(c *client) { s.leave(c, false) }
func (s *server) leave(c *client, deliberate bool) {
	code := c.roomCode
	closed, err := s.rooms.Leave(c.roomCode, c.participantID, deliberate, c)
	if err != nil {
		return
	}
	c.roomCode, c.participantID = "", ""
	if closed != nil {
		for _, other := range closed.Clients {
			_ = other.send("room_closed", map[string]string{"reason": closed.Reason})
		}
		return
	}
	s.broadcastRoom(code)
}
func (s *server) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for now := range ticker.C {
		for _, closed := range s.rooms.Cleanup(now) {
			for _, c := range closed.Clients {
				_ = c.send("room_closed", map[string]string{"reason": closed.Reason})
			}
		}
		// A disconnected guest may have expired without causing another room
		// event. Refresh surviving rooms so clients remove that stale entry.
		for _, code := range s.rooms.RoomCodes() { s.broadcastRoom(code) }
	}
}
