package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestCORSAllowsConfiguredOriginsAndRejectsOpaqueOrigins(t *testing.T) {
	s := newServer()
	allowed := "https://sites.google.com"
	health := httptest.NewRequest(http.MethodGet, "/health", nil)
	health.Header.Set("Origin", allowed)
	healthResult := httptest.NewRecorder()
	s.routes().ServeHTTP(healthResult, health)
	if healthResult.Code != http.StatusOK || healthResult.Header().Get("Access-Control-Allow-Origin") != allowed {
		t.Fatalf("allowed health request missing CORS headers: status=%d origin=%q", healthResult.Code, healthResult.Header().Get("Access-Control-Allow-Origin"))
	}

	preflight := httptest.NewRequest(http.MethodOptions, "/v1/rooms", nil)
	preflight.Header.Set("Origin", "http://127.0.0.1:8080")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodPost)
	preflight.Header.Set("Access-Control-Request-Headers", "content-type,authorization")
	preflightResult := httptest.NewRecorder()
	s.routes().ServeHTTP(preflightResult, preflight)
	if preflightResult.Code != http.StatusNoContent || preflightResult.Header().Get("Access-Control-Allow-Origin") != "http://127.0.0.1:8080" || !strings.Contains(preflightResult.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("allowed preflight response is incomplete: status=%d headers=%v", preflightResult.Code, preflightResult.Header())
	}

	for _, origin := range []string{"null", "https://untrusted.example"} {
		request := httptest.NewRequest(http.MethodGet, "/health", nil)
		request.Header.Set("Origin", origin)
		result := httptest.NewRecorder()
		s.routes().ServeHTTP(result, request)
		if result.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("opaque or untrusted origin %q received CORS access", origin)
		}
	}
}

func TestWebSocketOriginPolicy(t *testing.T) {
	s := newServer()
	h := httptest.NewServer(s.routes())
	defer h.Close()
	url := "ws" + strings.TrimPrefix(h.URL, "http") + "/ws"

	allowed, response, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": []string{"https://sites.google.com"}})
	if err != nil {
		t.Fatalf("allowed origin was rejected: response=%v err=%v", response, err)
	}
	_ = allowed.Close()

	denied, response, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": []string{"null"}})
	if denied != nil {
		_ = denied.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("opaque origin was not rejected: response=%v err=%v", response, err)
	}
}

func TestLocalDevelopmentOriginsAreAllowedForHTTPAndWebSocket(t *testing.T) {
	s := newServer()
	h := httptest.NewServer(s.routes())
	defer h.Close()
	url := "ws" + strings.TrimPrefix(h.URL, "http") + "/ws"

	for _, origin := range []string{"http://localhost:8000", "http://127.0.0.1:8000"} {
		request := httptest.NewRequest(http.MethodGet, "/health", nil)
		request.Header.Set("Origin", origin)
		result := httptest.NewRecorder()
		s.routes().ServeHTTP(result, request)
		if result.Code != http.StatusOK || result.Header().Get("Access-Control-Allow-Origin") != origin {
			t.Fatalf("development origin %q missing CORS headers: status=%d origin=%q", origin, result.Code, result.Header().Get("Access-Control-Allow-Origin"))
		}
		conn, response, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": []string{origin}})
		if err != nil {
			t.Fatalf("development origin %q was rejected by the WebSocket endpoint: response=%v err=%v", origin, response, err)
		}
		_ = conn.Close()
	}

	// The allowlist stays explicit: an unlisted localhost port gets no access.
	unlisted := httptest.NewRequest(http.MethodGet, "/health", nil)
	unlisted.Header.Set("Origin", "http://localhost:9999")
	unlistedResult := httptest.NewRecorder()
	s.routes().ServeHTTP(unlistedResult, unlisted)
	if unlistedResult.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unlisted localhost origin received CORS access")
	}
}

func openTestSocket(t *testing.T, base string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(base, "http") + "/ws"
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func sendTestMessage(t *testing.T, c *websocket.Conn, kind string, payload interface{}) {
	t.Helper()
	b, err := json.Marshal(envelope{Version: protocolVersion, Type: kind, Payload: mustJSON(payload)})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatal(err)
	}
}

func receiveType(t *testing.T, c *websocket.Conn, wanted string) map[string]json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = c.SetReadDeadline(deadline)
		_, body, err := c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var message struct {
			Type    string                     `json:"type"`
			Payload map[string]json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(body, &message); err != nil {
			t.Fatal(err)
		}
		if message.Type == wanted {
			return message.Payload
		}
	}
	t.Fatalf("did not receive %q", wanted)
	return nil
}

func waitForReadyPlayers(t *testing.T, c *websocket.Conn, wanted int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = c.SetReadDeadline(deadline)
		_, body, err := c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var message struct {
			Type    string `json:"type"`
			Payload struct {
				Participants []struct {
					Ready     bool `json:"ready"`
					Connected bool `json:"connected"`
				} `json:"participants"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(body, &message); err != nil {
			t.Fatal(err)
		}
		if message.Type != "room" {
			continue
		}
		count := 0
		for _, p := range message.Payload.Participants {
			if p.Connected && p.Ready {
				count++
			}
		}
		if count == wanted {
			return
		}
	}
	t.Fatalf("did not receive room state with %d ready players", wanted)
}

func TestWebSocketCreateJoinReadyAndStart(t *testing.T) {
	s := newServer()
	h := httptest.NewServer(s.routes())
	defer h.Close()
	hostConn := openTestSocket(t, h.URL)
	defer hostConn.Close()
	sendTestMessage(t, hostConn, "create", createRequest{Name: "Host", Config: testConfig()})
	hostSession := receiveType(t, hostConn, "session")
	var host struct {
		RoomCode      string `json:"roomCode"`
		ParticipantID string `json:"participantId"`
		Token         string `json:"token"`
	}
	if err := json.Unmarshal(hostSessionAsJSON(hostSession), &host); err != nil {
		t.Fatal(err)
	}
	if host.RoomCode == "" || host.Token == "" {
		t.Fatal("server did not issue a usable session")
	}

	guestConn := openTestSocket(t, h.URL)
	defer guestConn.Close()
	sendTestMessage(t, guestConn, "join", map[string]string{"roomCode": host.RoomCode, "name": "Guest"})
	guestSession := receiveType(t, guestConn, "session")
	var guest struct {
		ParticipantID string `json:"participantId"`
	}
	if err := json.Unmarshal(hostSessionAsJSON(guestSession), &guest); err != nil {
		t.Fatal(err)
	}
	if guest.ParticipantID == "" || guest.ParticipantID == host.ParticipantID {
		t.Fatal("guest session was not unique")
	}

	sendTestMessage(t, hostConn, "ready", map[string]bool{"ready": true})
	waitForReadyPlayers(t, hostConn, 1)
	sendTestMessage(t, guestConn, "ready", map[string]bool{"ready": true})
	waitForReadyPlayers(t, hostConn, 2)
	sendTestMessage(t, hostConn, "start", map[string]interface{}{})
	run := receiveType(t, hostConn, "race_start")
	if len(run["startAt"]) == 0 {
		t.Fatal("start event has no shared timestamp")
	}
	_ = receiveType(t, guestConn, "race_start")

	sendTestMessage(t, hostConn, "state", VehicleState{X: 12, Y: 42, Heading: .4, Speed: 120, Lap: 0, Progress: 50, ClientTime: time.Now().UnixMilli()})
	state := receiveType(t, guestConn, "state")
	if len(state["playerId"]) == 0 || len(state["state"]) == 0 {
		t.Fatal("state update was not relayed")
	}
}

func hostSessionAsJSON(payload map[string]json.RawMessage) []byte {
	b, _ := json.Marshal(payload)
	return b
}
