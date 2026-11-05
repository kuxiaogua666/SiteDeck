package hub

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"sitedeck/internal/access"
	"sitedeck/internal/identity"
	"sitedeck/internal/protocol"
	"sitedeck/internal/transport"
)

type agent struct {
	socket *transport.Socket
	device string
}
type channel struct {
	id              string
	agent           *agent
	client          *transport.Socket
	bytes           uint64
	createdAt       time.Time
	authenticatedAt *time.Time
	lastActivity    time.Time
	transport       string
	remoteAddress   string
	grant           string
	expiresAt       *time.Time
	expiryTimer     *time.Timer
}
type Hub struct {
	mu            sync.Mutex
	agents        map[string]*agent
	channels      map[string]*channel
	registration  string
	MaxChannels   int
	state         state
	statePath     string
	admin         string
	adminSessions map[string]adminSession
	loginAttempts map[string]loginAttempt
}

func New(registration string) *Hub {
	s, err := newState()
	if err != nil {
		panic(err)
	}
	return &Hub{agents: map[string]*agent{}, channels: map[string]*channel{}, registration: registration, MaxChannels: 8, state: s, adminSessions: map[string]adminSession{}, loginAttempts: map[string]loginAttempt{}}
}
func Open(registration, adminSecret, statePath string) (*Hub, error) {
	if len(adminSecret) < 32 {
		return nil, fmt.Errorf("Hub admin secret must contain at least 32 characters")
	}
	if subtle.ConstantTimeCompare([]byte(adminSecret), []byte(registration)) == 1 {
		return nil, fmt.Errorf("management and Agent registration secrets must be different")
	}
	s, err := loadState(statePath)
	if err != nil {
		return nil, err
	}
	h := New(registration)
	h.state = s
	h.statePath = statePath
	h.admin = adminSecret
	return h, nil
}
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","protocolVersion":1}`))
	})
	mux.HandleFunc("/ws", h.serve)
	h.adminRoutes(mux)
	return mux
}
func (h *Hub) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser origins are not supported on the Agent protocol", http.StatusForbidden)
		return
	}
	c, err := (&websocket.Upgrader{HandshakeTimeout: 10 * time.Second}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s := transport.Wrap(c)
	defer s.Close()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	first, err := s.Read()
	if err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	switch first.Type {
	case "register":
		h.register(s, r, first)
	case "connect":
		h.connect(s, r, first)
	default:
		_ = s.Send(protocol.Signal{Type: "error", Reason: "INVALID_MESSAGE"})
	}
}
func (h *Hub) register(s *transport.Socket, r *http.Request, first protocol.Signal) {
	if first.Device == "" || len(first.Device) > 128 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+h.registration)) != 1 {
		_ = s.Send(protocol.Signal{Type: "error", Reason: "REGISTRATION_DENIED"})
		return
	}
	h.mu.Lock()
	if _, exists := h.agents[first.Device]; exists {
		h.mu.Unlock()
		_ = s.Send(protocol.Signal{Type: "error", Reason: "DEVICE_ALREADY_REGISTERED"})
		return
	}
	if len(h.state.Devices) >= 4096 {
		if _, known := h.state.Devices[first.Device]; !known {
			h.mu.Unlock()
			_ = s.Send(protocol.Signal{Type: "error", Reason: "DEVICE_LIMIT_REACHED"})
			return
		}
	}
	next := h.state.clone()
	d, known := next.Devices[first.Device]
	now := time.Now().UTC()
	if !known {
		name := strings.TrimSpace(first.Name)
		if !validName(name) {
			name = first.Device
		}
		d = Device{ID: first.Device, Name: name, AllowPaired: true, FirstSeen: now}
	}
	d.LastSeen = now
	d.OS = bounded(first.OS, 40)
	d.Arch = bounded(first.Arch, 40)
	d.Version = bounded(first.Version, 40)
	d.Capabilities = normalizeCapabilities(first.Capabilities)
	d.AccessProtocol = first.AccessProtocol
	next.Devices[d.ID] = d
	if err := h.commitLocked(next); err != nil {
		h.mu.Unlock()
		log.Printf("cannot persist device registration: %v", err)
		_ = s.Send(protocol.Signal{Type: "error", Reason: "STORAGE_ERROR"})
		return
	}
	a := &agent{socket: s, device: first.Device}
	h.agents[a.device] = a
	publicKey := base64.RawStdEncoding.EncodeToString(h.signingKey().Public().(ed25519.PublicKey))
	h.mu.Unlock()
	defer h.removeAgent(a)
	if s.Send(protocol.Signal{Type: "registered", Device: a.device, SigningKey: publicKey}) != nil {
		return
	}
	for {
		_ = s.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		m, err := s.Read()
		if err != nil {
			return
		}
		if m.Type == "heartbeat" {
			h.mu.Lock()
			d := h.state.Devices[a.device]
			d.LastSeen = time.Now().UTC()
			h.state.Devices[a.device] = d
			h.mu.Unlock()
			if s.Send(protocol.Signal{Type: "heartbeat"}) != nil {
				return
			}
			continue
		}
		if !h.route(m, s, a) {
			return
		}
	}
}
func (h *Hub) connect(s *transport.Socket, r *http.Request, first protocol.Signal) {
	h.mu.Lock()
	a := h.agents[first.Device]
	if a == nil {
		h.mu.Unlock()
		_ = s.Send(protocol.Signal{Type: "error", Reason: "DEVICE_OFFLINE"})
		return
	}
	d := h.state.Devices[first.Device]
	if d.Blocked {
		h.mu.Unlock()
		_ = s.Send(protocol.Signal{Type: "error", Reason: "DEVICE_BLOCKED"})
		return
	}
	grant, reason := h.authorizeLocked(d, first.AccessToken)
	if reason != "" {
		h.mu.Unlock()
		_ = s.Send(protocol.Signal{Type: "error", Reason: reason})
		return
	}
	if grant != nil && d.AccessProtocol != 1 {
		h.mu.Unlock()
		_ = s.Send(protocol.Signal{Type: "error", Reason: "AGENT_UPGRADE_REQUIRED"})
		return
	}
	count := 0
	for _, ch := range h.channels {
		if ch.agent == a {
			count++
		}
	}
	if count >= h.MaxChannels || len(h.channels) >= 1024 {
		h.mu.Unlock()
		_ = s.Send(protocol.Signal{Type: "error", Reason: "CHANNEL_QUOTA_EXCEEDED"})
		return
	}
	now := time.Now().UTC()
	ch := &channel{id: identity.Random(16), agent: a, client: s, createdAt: now, lastActivity: now, transport: "negotiating", remoteAddress: r.RemoteAddr}
	message := protocol.Signal{Type: "connected", Device: a.device, Channel: ch.id}
	if grant != nil {
		ch.grant = grant.ID
		expires := grant.ExpiresAt
		ch.expiresAt = &expires
		message.AccessRequired = true
		var err error
		message.AccessTicket, err = access.Sign(h.signingKey(), access.Claims{Version: 1, Device: a.device, Channel: ch.id, Grant: grant.ID, Scopes: grant.Scopes, ExpiresAtMS: expires.UnixMilli(), HandshakeUntilMS: now.Add(3 * time.Minute).UnixMilli()})
		if err != nil {
			h.mu.Unlock()
			_ = s.Send(protocol.Signal{Type: "error", Reason: "AUTHORIZATION_ERROR"})
			return
		}
	}
	h.channels[ch.id] = ch
	if ch.expiresAt != nil {
		ch.expiryTimer = time.AfterFunc(time.Until(*ch.expiresAt), func() { h.closeChannel(ch, "ACCESS_EXPIRED") })
	}
	h.mu.Unlock()
	defer h.closeChannel(ch, "PEER_DISCONNECTED")
	if a.socket.Send(message) != nil || s.Send(message) != nil {
		return
	}
	for {
		_ = s.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		m, err := s.Read()
		if err != nil {
			return
		}
		if m.Channel != ch.id {
			return
		}
		if m.Type == "heartbeat" {
			if s.Send(protocol.Signal{Type: "heartbeat", Channel: ch.id}) != nil {
				return
			}
			continue
		}
		if !h.route(m, s, nil) {
			return
		}
	}
}
func (h *Hub) authorizeLocked(d Device, token string) (*Grant, string) {
	if token == "" {
		if d.AllowPaired {
			return nil, ""
		}
		return nil, "ACCESS_REQUIRED"
	}
	hash := sha256.Sum256([]byte(token))
	encoded := hex.EncodeToString(hash[:])
	now := time.Now()
	for _, stored := range h.state.Grants {
		if subtle.ConstantTimeCompare([]byte(stored.Hash), []byte(encoded)) == 1 {
			g := stored.Grant
			if g.Device != d.ID || g.RevokedAt != nil {
				return nil, "ACCESS_DENIED"
			}
			if !now.Before(g.ExpiresAt) {
				return nil, "ACCESS_EXPIRED"
			}
			return &g, ""
		}
	}
	return nil, "ACCESS_DENIED"
}
func (h *Hub) route(m protocol.Signal, s *transport.Socket, a *agent) bool {
	switch m.Type {
	case "offer", "answer", "relay", "data", "close", "channel.ready":
	default:
		return false
	}
	if len(m.Data) > 16<<10 || len(m.SDP) > 64<<10 || len(m.Attempt) > 64 {
		return false
	}
	h.mu.Lock()
	ch := h.channels[m.Channel]
	if ch == nil {
		h.mu.Unlock()
		return a != nil
	}
	var peer *transport.Socket
	if a != nil && ch.agent == a {
		peer = ch.client
	} else if a == nil && ch.client == s {
		peer = ch.agent.socket
	} else {
		h.mu.Unlock()
		return false
	}
	if m.Type == "channel.ready" {
		if a == nil {
			h.mu.Unlock()
			return false
		}
		now := time.Now().UTC()
		ch.authenticatedAt = &now
		if m.Transport == "p2p" || m.Transport == "turn" || m.Transport == "relay" {
			ch.transport = m.Transport
		}
		h.mu.Unlock()
		return true
	}
	if m.Type == "relay" {
		ch.transport = "relay"
	}
	ch.lastActivity = time.Now().UTC()
	ch.bytes += uint64(len(m.Data))
	h.mu.Unlock()
	if m.Type == "close" {
		h.closeChannel(ch, "PEER_DISCONNECTED")
		return a != nil
	}
	if peer.Send(m) != nil {
		h.closeChannel(ch, "PEER_DISCONNECTED")
		return a != nil
	}
	return true
}
func (h *Hub) closeChannel(ch *channel, reason string) {
	h.mu.Lock()
	if h.channels[ch.id] != ch {
		h.mu.Unlock()
		return
	}
	delete(h.channels, ch.id)
	count := ch.bytes
	if ch.expiryTimer != nil {
		ch.expiryTimer.Stop()
	}
	h.mu.Unlock()
	_ = ch.client.Send(protocol.Signal{Type: "close", Channel: ch.id, Reason: reason})
	_ = ch.client.Close()
	_ = ch.agent.socket.Send(protocol.Signal{Type: "close", Channel: ch.id, Reason: reason})
	log.Printf("channel closed device=%s channel=%s reason=%s relayPayloadBytes=%d", ch.agent.device, ch.id, reason, count)
}
func (h *Hub) removeAgent(a *agent) {
	h.mu.Lock()
	if h.agents[a.device] == a {
		delete(h.agents, a.device)
		d := h.state.Devices[a.device]
		d.LastSeen = time.Now().UTC()
		h.state.Devices[a.device] = d
		if err := saveState(h.statePath, h.state); err != nil {
			log.Printf("cannot persist device last-seen time: %v", err)
		}
	}
	var list []*channel
	for _, ch := range h.channels {
		if ch.agent == a {
			list = append(list, ch)
		}
	}
	h.mu.Unlock()
	for _, ch := range list {
		h.closeChannel(ch, "DEVICE_OFFLINE")
	}
}
func (h *Hub) Close() {
	h.mu.Lock()
	var list []*agent
	for _, a := range h.agents {
		list = append(list, a)
	}
	h.mu.Unlock()
	for _, a := range list {
		_ = a.socket.Close()
	}
}
func validName(name string) bool {
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 80 {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func bounded(s string, max int) string {
	if len(s) > max || !utf8.ValidString(s) {
		return ""
	}
	return s
}
func normalizeCapabilities(scopes []string) []string {
	result := []string{}
	for _, scope := range []string{"host", "serial", "ssh"} {
		for _, candidate := range scopes {
			if scope == candidate {
				result = append(result, scope)
				break
			}
		}
	}
	sort.Strings(result)
	return result
}
