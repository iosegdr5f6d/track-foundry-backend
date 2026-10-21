package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

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
