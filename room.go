package main

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	protocolVersion     = 1
	maxParticipants     = 12
	disconnectGrace     = 2 * time.Minute
	roomInactivityLimit = 30 * time.Minute
)

// RaceConfig intentionally contains only values that change the shared race.
// Camera, audio, and other presentation preferences stay in the browser.
type RaceConfig struct {
	Laps        int    `json:"laps"`
	Vehicle     string `json:"vehicle"`
	Damage      string `json:"damage"`
	Pits        string `json:"pits"`
	Wear        string `json:"wear"`
	Weather     string `json:"weather"`
	TimeOfDay   string `json:"timeOfDay"`
	Assist      string `json:"assist"`
	Surface     string `json:"surface"`
	MotoAids    string `json:"motoAids"`
	MotoFalls   string `json:"motoFalls"`
	MotoWheelie string `json:"motoWheelie"`
}

type SharedConfig struct {
	Version   int        `json:"version"`
	TrackCode string     `json:"trackCode"`
	Race      RaceConfig `json:"race"`
}

type trackPayload struct {
	Seed    string            `json:"s"`
	Size    float64           `json:"z"`
	Twist   float64           `json:"t"`
	Corners float64           `json:"c"`
	Width   float64           `json:"w"`
	Elev    float64           `json:"e"`
	Bank    float64           `json:"b"`
	User    []float64         `json:"u"`
	Banks   []json.RawMessage `json:"k"`
	Rally   []json.RawMessage `json:"r"`
	Drawn   [][]float64       `json:"d"`
}

func oneOf(value string, values ...string) bool {
	for _, allowed := range values {
		if value == allowed {
			return true
		}
	}
	return false
}

func (c SharedConfig) Validate() error {
	if c.Version != protocolVersion {
		return errors.New("unsupported configuration version")
	}
	if len(c.TrackCode) < 8 || len(c.TrackCode) > 28000 || !strings.HasPrefix(c.TrackCode, "TF1.") {
		return errors.New("invalid track configuration")
	}
	encoded := c.TrackCode[4:]
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 || len(raw) > 21000 { return errors.New("malformed track configuration") }
	var track trackPayload
	if err := json.Unmarshal(raw, &track); err != nil || len(track.Seed) > 24 || track.Size < 1600 || track.Size > 6000 || track.Twist < 10 || track.Twist > 60 || track.Corners < 8 || track.Corners > 18 || track.Width < 100 || track.Width > 170 || track.Elev < 0 || track.Elev > 100 || track.Bank < 0 || track.Bank > 100 || len(track.User) > 48 || len(track.Banks) > 18 || len(track.Rally) > 12 || len(track.Drawn) > 180 { return errors.New("malformed track configuration") }
	for _, point := range track.Drawn { if len(point) != 2 || math.Abs(point[0]) > 10000 || math.Abs(point[1]) > 10000 { return errors.New("malformed drawn track") } }
	r := c.Race
	if r.Laps != 0 && r.Laps != 3 && r.Laps != 5 && r.Laps != 10 {
		return errors.New("invalid lap count")
	}
	if !oneOf(r.Vehicle, "f1", "rally", "moto") || !oneOf(r.Damage, "off", "normal", "brutal") ||
		!oneOf(r.Pits, "on", "off") || !oneOf(r.Wear, "off", "normal", "high") ||
		!oneOf(r.Weather, "dry", "light", "storm", "changing") || !oneOf(r.TimeOfDay, "day", "dusk", "night") ||
		!oneOf(r.Assist, "off", "std", "full") || !oneOf(r.Surface, "tarmac", "gravel") ||
		!oneOf(r.MotoAids, "off", "std", "full") || !oneOf(r.MotoFalls, "on", "off") || !oneOf(r.MotoWheelie, "on", "off") {
		return errors.New("invalid race setting")
	}
	return nil
}

type VehicleState struct {
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Heading    float64 `json:"heading"`
	Speed      float64 `json:"speed"`
	VX         float64 `json:"vx"`
	VY         float64 `json:"vy"`
	Lap        int     `json:"lap"`
	Progress   float64 `json:"progress"`
	Finished   bool    `json:"finished"`
	ClientTime int64   `json:"clientTime"`
}

func (s VehicleState) Validate() error {
	if math.IsNaN(s.X) || math.IsNaN(s.Y) || math.IsNaN(s.Heading) || math.IsNaN(s.Speed) || math.IsNaN(s.VX) || math.IsNaN(s.VY) || math.IsNaN(s.Progress) ||
		math.IsInf(s.X, 0) || math.IsInf(s.Y, 0) || math.IsInf(s.Heading, 0) || math.IsInf(s.Speed, 0) || math.IsInf(s.VX, 0) || math.IsInf(s.VY, 0) || math.IsInf(s.Progress, 0) {
		return errors.New("non-finite vehicle state")
	}
	if math.Abs(s.X) > 50000 || math.Abs(s.Y) > 50000 || math.Abs(s.Heading) > 50 || math.Abs(s.Speed) > 3000 || math.Abs(s.VX) > 3000 || math.Abs(s.VY) > 3000 || s.Lap < -1 || s.Lap > 1000 || math.Abs(s.Progress) > 10000000 {
		return errors.New("vehicle state is outside allowed bounds")
	}
	return nil
}

type FinishState struct {
	Lap        int     `json:"lap"`
	Progress   float64 `json:"progress"`
	FinishTime float64 `json:"finishTime"`
}

func (f FinishState) Validate() error {
	if f.Lap < 0 || f.Lap > 1000 || math.IsNaN(f.Progress) || math.IsInf(f.Progress, 0) || math.Abs(f.Progress) > 10000000 || math.IsNaN(f.FinishTime) || math.IsInf(f.FinishTime, 0) || f.FinishTime < 0 || f.FinishTime > 24*60*60 {
		return errors.New("invalid finish state")
	}
	return nil
}

type Participant struct {
	ID          string
	Token       string
	Name        string
	Ready       bool
	Connected   bool
	LastSeen    time.Time
	LastStateAt time.Time
	State       *VehicleState
	Finish      *FinishState
	FinishOrder int
	Connection  *client
}

type RaceState struct {
	Phase        string `json:"phase"`
	StartAt      int64  `json:"startAt,omitempty"`
	ConfigLocked bool   `json:"configLocked"`
	FinishCount  int    `json:"-"`
}

type Room struct {
	Code         string
	HostID       string
	Config       SharedConfig
	Participants map[string]*Participant
	Race         RaceState
	UpdatedAt    time.Time
}

type ParticipantView struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Host        bool          `json:"host"`
	Ready       bool          `json:"ready"`
	Connected   bool          `json:"connected"`
	State       *VehicleState `json:"state,omitempty"`
	Finish      *FinishState  `json:"finish,omitempty"`
	FinishOrder int           `json:"finishOrder,omitempty"`
}

type RoomSnapshot struct {
	Code         string            `json:"code"`
	HostID       string            `json:"hostId"`
	Config       SharedConfig      `json:"config"`
	Race         RaceState         `json:"race"`
	Participants []ParticipantView `json:"participants"`
	ServerTime   int64             `json:"serverTime"`
}

type Session struct {
	RoomCode      string        `json:"roomCode"`
	ParticipantID string        `json:"participantId"`
	Token         string        `json:"token"`
	Room          *RoomSnapshot `json:"room"`
}

type ClosedRoom struct {
	Clients []*client
	Reason  string
}

type RoomManager struct {
	mu    sync.Mutex
	rooms map[string]*Room
}

func NewRoomManager() *RoomManager { return &RoomManager{rooms: make(map[string]*Room)} }

func randomText(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.TrimRight(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), "="), nil
}

func normalizeCode(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if len(name) < 1 || len(name) > 24 {
		return "", errors.New("display name must be 1 to 24 characters")
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return "", errors.New("display name contains an unsupported character")
		}
	}
	return name, nil
}

func (m *RoomManager) makeRoomCodeLocked() (string, error) {
	for i := 0; i < 16; i++ {
		code, err := randomText(5)
		if err != nil {
			return "", err
		}
		code = code[:8]
		if _, exists := m.rooms[code]; !exists {
			return code, nil
		}
	}
	return "", errors.New("could not allocate room code")
}

func (m *RoomManager) Create(name string, config SharedConfig, c *client) (Session, error) {
	if err := config.Validate(); err != nil {
		return Session{}, err
	}
	name, err := validName(name)
	if err != nil {
		return Session{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	code, err := m.makeRoomCodeLocked()
	if err != nil {
		return Session{}, err
	}
	id, err := randomText(10)
	if err != nil {
		return Session{}, err
	}
	token, err := randomText(24)
	if err != nil {
		return Session{}, err
	}
	now := time.Now()
	p := &Participant{ID: id, Token: token, Name: name, Connected: c != nil, LastSeen: now, Connection: c}
	r := &Room{Code: code, HostID: id, Config: config, Participants: map[string]*Participant{id: p}, Race: RaceState{Phase: "lobby"}, UpdatedAt: now}
	m.rooms[code] = r
	if c != nil {
		c.roomCode, c.participantID = code, id
	}
	s := m.snapshotLocked(r)
	return Session{RoomCode: code, ParticipantID: id, Token: token, Room: &s}, nil
}

func (m *RoomManager) Join(code, name, token string, c *client) (Session, error) {
	code = normalizeCode(code)
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[code]
	if r == nil {
		return Session{}, errors.New("room unavailable or expired")
	}
	if token != "" {
		for _, p := range r.Participants {
			if p.Token == token {
				if c != nil {
					m.replaceConnectionLocked(r, p, c)
				}
				p.LastSeen = time.Now()
				r.UpdatedAt = p.LastSeen
				s := m.snapshotLocked(r)
				return Session{RoomCode: code, ParticipantID: p.ID, Token: p.Token, Room: &s}, nil
			}
		}
	}
	if r.Race.Phase != "lobby" || r.Race.ConfigLocked {
		return Session{}, errors.New("race has already started")
	}
	if len(r.Participants) >= maxParticipants {
		return Session{}, errors.New("room is full")
	}
	name, err := validName(name)
	if err != nil {
		return Session{}, err
	}
	id, err := randomText(10)
	if err != nil {
		return Session{}, err
	}
	newToken, err := randomText(24)
	if err != nil {
		return Session{}, err
	}
	now := time.Now()
	p := &Participant{ID: id, Token: newToken, Name: name, Connected: c != nil, LastSeen: now, Connection: c}
	r.Participants[id] = p
	r.UpdatedAt = now
	if c != nil {
		c.roomCode, c.participantID = code, id
	}
	s := m.snapshotLocked(r)
	return Session{RoomCode: code, ParticipantID: id, Token: newToken, Room: &s}, nil
}

func (m *RoomManager) Authenticate(code, token string, c *client) (Session, error) {
	code = normalizeCode(code)
	if token == "" {
		return Session{}, errors.New("missing session token")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[code]
	if r == nil {
		return Session{}, errors.New("room unavailable or expired")
	}
	for _, p := range r.Participants {
		if p.Token == token {
			m.replaceConnectionLocked(r, p, c)
			now := time.Now()
			p.LastSeen = now
			r.UpdatedAt = now
			s := m.snapshotLocked(r)
			return Session{RoomCode: code, ParticipantID: p.ID, Token: p.Token, Room: &s}, nil
		}
	}
	return Session{}, errors.New("invalid session")
}

func (m *RoomManager) replaceConnectionLocked(r *Room, p *Participant, c *client) {
	if p.Connection != nil && p.Connection != c {
		p.Connection.closeQuietly()
	}
	p.Connection, p.Connected = c, true
	if c != nil {
		c.roomCode, c.participantID = r.Code, p.ID
	}
}

func (m *RoomManager) SetReady(code, id string, ready bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, p, err := m.memberLocked(code, id)
	if err != nil {
		return err
	}
	if r.Race.Phase != "lobby" {
		return errors.New("readiness is locked after race start")
	}
	p.Ready = ready
	r.UpdatedAt = time.Now()
	return nil
}

func (m *RoomManager) UpdateConfig(code, id string, config SharedConfig) error {
	if err := config.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, _, err := m.memberLocked(code, id)
	if err != nil {
		return err
	}
	if r.HostID != id {
		return errors.New("only the host can update shared setup")
	}
	if r.Race.ConfigLocked || r.Race.Phase != "lobby" {
		return errors.New("shared setup is locked")
	}
	r.Config = config
	for _, p := range r.Participants {
		p.Ready = false
	}
	r.UpdatedAt = time.Now()
	return nil
}

func (m *RoomManager) Start(code, id string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, _, err := m.memberLocked(code, id)
	if err != nil {
		return 0, err
	}
	if r.HostID != id {
		return 0, errors.New("only the host can start the race")
	}
	if r.Race.Phase != "lobby" || r.Race.ConfigLocked {
		return 0, errors.New("race is already starting or running")
	}
	connected := 0
	for _, p := range r.Participants {
		if p.Connected {
			connected++
			if !p.Ready {
				return 0, errors.New("all connected players must be ready")
			}
		}
	}
	if connected < 2 {
		return 0, errors.New("at least two connected players are required")
	}
	startAt := time.Now().Add(5 * time.Second).UnixMilli()
	r.Race = RaceState{Phase: "countdown", StartAt: startAt, ConfigLocked: true}
	r.UpdatedAt = time.Now()
	return startAt, nil
}

func (m *RoomManager) SetVehicleState(code, id string, state VehicleState) ([]*client, error) {
	if err := state.Validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, p, err := m.memberLocked(code, id)
	if err != nil {
		return nil, err
	}
	if r.Race.Phase != "countdown" && r.Race.Phase != "race" {
		return nil, errors.New("race has not started")
	}
	now := time.Now()
	if !p.LastStateAt.IsZero() && now.Sub(p.LastStateAt) < 50*time.Millisecond {
		return nil, errors.New("state update rate exceeded")
	}
	p.LastStateAt, p.LastSeen = now, now
	p.State = &state
	if now.UnixMilli() >= r.Race.StartAt {
		r.Race.Phase = "race"
	}
	return m.clientsLocked(r, id), nil
}

func (m *RoomManager) Finish(code, id string, finish FinishState) error {
	if err := finish.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, p, err := m.memberLocked(code, id)
	if err != nil {
		return err
	}
	if time.Now().UnixMilli() >= r.Race.StartAt && r.Race.Phase == "countdown" {
		r.Race.Phase = "race"
	}
	if r.Race.Phase != "race" {
		return errors.New("race has not started")
	}
	if p.Finish == nil {
		r.Race.FinishCount++
		p.FinishOrder = r.Race.FinishCount
		p.Finish = &finish
	}
	p.LastSeen, r.UpdatedAt = time.Now(), time.Now()
	return nil
}

func (m *RoomManager) Leave(code, id string, deliberate bool, connection *client) (*ClosedRoom, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, p, err := m.memberLocked(code, id)
	if err != nil {
		return nil, err
	}
	if !deliberate {
		// An older socket can finish closing after a reconnect. It must never
		// mark the replacement socket as disconnected.
		if connection != nil && p.Connection == connection {
			p.Connection = nil
			p.Connected = false
			p.LastSeen = time.Now()
			r.UpdatedAt = p.LastSeen
		}
		return nil, nil
	}
	if r.HostID == id {
		closed := &ClosedRoom{Clients: m.clientsLocked(r, ""), Reason: "The host left and closed this room."}
		delete(m.rooms, code)
		return closed, nil
	}
	delete(r.Participants, id)
	r.UpdatedAt = time.Now()
	return nil, nil
}

func (m *RoomManager) GetSnapshot(code string) (RoomSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[normalizeCode(code)]
	if r == nil {
		return RoomSnapshot{}, errors.New("room unavailable or expired")
	}
	return m.snapshotLocked(r), nil
}

func (m *RoomManager) AuthorizedSnapshot(code, token string) (RoomSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[normalizeCode(code)]
	if r == nil {
		return RoomSnapshot{}, errors.New("room unavailable or expired")
	}
	for _, p := range r.Participants {
		if p.Token == token {
			return m.snapshotLocked(r), nil
		}
	}
	return RoomSnapshot{}, errors.New("invalid session")
}

func (m *RoomManager) BroadcastTargets(code string, exceptID string) ([]*client, RoomSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[normalizeCode(code)]
	if r == nil {
		return nil, RoomSnapshot{}, errors.New("room unavailable or expired")
	}
	return m.clientsLocked(r, exceptID), m.snapshotLocked(r), nil
}

func (m *RoomManager) clientsLocked(r *Room, exceptID string) []*client {
	clients := make([]*client, 0, len(r.Participants))
	for id, p := range r.Participants {
		if id != exceptID && p.Connected && p.Connection != nil {
			clients = append(clients, p.Connection)
		}
	}
	return clients
}

func (m *RoomManager) memberLocked(code, id string) (*Room, *Participant, error) {
	r := m.rooms[normalizeCode(code)]
	if r == nil {
		return nil, nil, errors.New("room unavailable or expired")
	}
	p := r.Participants[id]
	if p == nil {
		return nil, nil, errors.New("invalid session")
	}
	return r, p, nil
}

func (m *RoomManager) snapshotLocked(r *Room) RoomSnapshot {
	participants := make([]ParticipantView, 0, len(r.Participants))
	for _, p := range r.Participants {
		view := ParticipantView{ID: p.ID, Name: p.Name, Host: p.ID == r.HostID, Ready: p.Ready, Connected: p.Connected, FinishOrder: p.FinishOrder}
		if p.State != nil {
			state := *p.State
			view.State = &state
		}
		if p.Finish != nil {
			f := *p.Finish
			view.Finish = &f
		}
		participants = append(participants, view)
	}
	sort.Slice(participants, func(i, j int) bool {
		if participants[i].Host != participants[j].Host {
			return participants[i].Host
		}
		return strings.ToLower(participants[i].Name) < strings.ToLower(participants[j].Name)
	})
	return RoomSnapshot{Code: r.Code, HostID: r.HostID, Config: r.Config, Race: r.Race, Participants: participants, ServerTime: time.Now().UnixMilli()}
}

func (m *RoomManager) Cleanup(now time.Time) []ClosedRoom {
	m.mu.Lock()
	defer m.mu.Unlock()
	closed := []ClosedRoom{}
	for code, r := range m.rooms {
		hostExpired := false
		for id, p := range r.Participants {
			if !p.Connected && now.Sub(p.LastSeen) > disconnectGrace {
				if id == r.HostID {
					hostExpired = true
				}
				delete(r.Participants, id)
			}
		}
		if hostExpired || len(r.Participants) == 0 || now.Sub(r.UpdatedAt) > roomInactivityLimit {
			reason := "This room expired after inactivity."
			if hostExpired {
				reason = "The host did not reconnect, so this room was closed."
			}
			closed = append(closed, ClosedRoom{Clients: m.clientsLocked(r, ""), Reason: reason})
			delete(m.rooms, code)
		}
	}
	return closed
}

func (m *RoomManager) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fmt.Sprintf("%d active rooms", len(m.rooms))
}

func (m *RoomManager) RoomCodes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	codes := make([]string, 0, len(m.rooms))
	for code := range m.rooms { codes = append(codes, code) }
	return codes
}
