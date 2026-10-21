package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testConfig() SharedConfig {
	track, _ := json.Marshal(map[string]interface{}{ "s": "TEST", "z": 3200, "t": 35, "c": 12, "w": 130, "e": 35, "b": 35 })
	return SharedConfig{Version: 1, TrackCode: "TF1." + base64.StdEncoding.EncodeToString(track), Race: RaceConfig{Laps: 3, Vehicle: "f1", Damage: "normal", Pits: "on", Wear: "normal", Weather: "dry", TimeOfDay: "day", Assist: "std", Surface: "tarmac", MotoAids: "std", MotoFalls: "on", MotoWheelie: "on"}}
}

func TestCreateJoinAndResume(t *testing.T) {
	m := NewRoomManager()
	host, err := m.Create("Host", testConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(host.RoomCode) != 8 {
		t.Fatalf("unexpected room code: %q", host.RoomCode)
	}
	guest, err := m.Join(strings.ToLower(host.RoomCode), "Guest", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if guest.ParticipantID == host.ParticipantID {
		t.Fatal("join created duplicate participant id")
	}
	resumed, err := m.Join(host.RoomCode, "ignored", guest.Token, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ParticipantID != guest.ParticipantID || len(resumed.Room.Participants) != 2 {
		t.Fatal("session resumption did not reuse the participant")
	}
}

func TestStartRequiresHostAndReadyPlayers(t *testing.T) {
	m := NewRoomManager()
	host, _ := m.Create("Host", testConfig(), &client{})
	guest, _ := m.Join(host.RoomCode, "Guest", "", &client{})
	if _, err := m.Start(host.RoomCode, guest.ParticipantID); err == nil {
		t.Fatal("guest started race")
	}
	if err := m.SetReady(host.RoomCode, host.ParticipantID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(host.RoomCode, host.ParticipantID); err == nil {
		t.Fatal("started without all ready")
	}
	if err := m.SetReady(host.RoomCode, guest.ParticipantID, true); err != nil {
		t.Fatal(err)
	}
	startAt, err := m.Start(host.RoomCode, host.ParticipantID)
	if err != nil {
		t.Fatal(err)
	}
	if startAt < time.Now().Add(4*time.Second).UnixMilli() {
		t.Fatal("start was not scheduled in the future")
	}
}

func TestHostOnlyConfigAndStateValidation(t *testing.T) {
	m := NewRoomManager()
	host, _ := m.Create("Host", testConfig(), nil)
	guest, _ := m.Join(host.RoomCode, "Guest", "", nil)
	if err := m.UpdateConfig(host.RoomCode, guest.ParticipantID, testConfig()); err == nil {
		t.Fatal("guest updated config")
	}
	bad := testConfig()
	bad.TrackCode = "nope"
	if err := m.UpdateConfig(host.RoomCode, host.ParticipantID, bad); err == nil {
		t.Fatal("accepted invalid config")
	}
	if err := (VehicleState{X: 1e9}).Validate(); err == nil {
		t.Fatal("accepted invalid state")
	}
}

func TestCleanupRemovesDisconnectedRoom(t *testing.T) {
	m := NewRoomManager()
	host, _ := m.Create("Host", testConfig(), nil)
	m.mu.Lock()
	r := m.rooms[host.RoomCode]
	r.Participants[host.ParticipantID].Connected = false
	r.Participants[host.ParticipantID].LastSeen = time.Now().Add(-disconnectGrace - time.Second)
	m.mu.Unlock()
	closed := m.Cleanup(time.Now())
	if len(closed) != 1 {
		t.Fatalf("expected room cleanup, got %d", len(closed))
	}
	if _, err := m.GetSnapshot(host.RoomCode); err == nil {
		t.Fatal("expired room remained")
	}
}
