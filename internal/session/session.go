package session

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"sitedeck/internal/identity"
	"sitedeck/internal/protocol"
)

const BufferSize = 1 << 20

type Session struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Name        string    `json:"name"`
	Created     time.Time `json:"createdAt"`
	mu          sync.Mutex
	writeMu     sync.Mutex
	operationMu sync.Mutex
	data        []byte
	start       uint64
	tail        uint64
	events      []span
	changed     chan struct{}
	eof         bool
	exit        *int
	last        time.Time
	members     map[string]bool
	writer      io.Writer
	closer      io.Closer
	resize      func(uint16, uint16) error
	closeOnce   sync.Once
}
type span struct {
	start, end uint64
	direction  string
	time       time.Time
}
type Event struct {
	Time      time.Time `json:"time"`
	Direction string    `json:"direction"`
	Data      []byte    `json:"data"`
	Cursor    string    `json:"cursor"`
}

func New(kind, name string, writer io.Writer, closer io.Closer, resize func(uint16, uint16) error) *Session {
	return &Session{ID: identity.Random(16), Kind: kind, Name: name, Created: time.Now(), last: time.Now(), changed: make(chan struct{}), members: map[string]bool{}, writer: writer, closer: closer, resize: resize}
}
func (s *Session) Write(b []byte) (int, error) {
	s.append(b, "rx")
	return len(b), nil
}
func (s *Session) append(b []byte, direction string) {
	if len(b) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make([]byte, BufferSize)
	}
	begin := s.tail
	s.tail += uint64(len(b))
	writeAt := begin
	if len(b) > BufferSize {
		drop := len(b) - BufferSize
		b = b[drop:]
		writeAt += uint64(drop)
	}
	i := int(writeAt % BufferSize)
	n := copy(s.data[i:], b)
	copy(s.data, b[n:])
	if s.tail-s.start > BufferSize {
		s.start = s.tail - BufferSize
	}
	if s.Kind == "serial" {
		s.events = append(s.events, span{start: begin, end: s.tail, direction: direction, time: time.Now().UTC()})
		for len(s.events) > 0 && s.events[0].end <= s.start {
			s.events = s.events[1:]
		}
		if len(s.events) > 4096 {
			s.events = append([]span(nil), s.events[len(s.events)-4096:]...)
			if s.start < s.events[0].start {
				s.start = s.events[0].start
			}
		}
	}
	close(s.changed)
	s.changed = make(chan struct{})
}
func (s *Session) End(exit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.eof {
		s.eof = true
		s.exit = &exit
		close(s.changed)
		s.changed = make(chan struct{})
	}
}
func (s *Session) Close() error {
	var err error
	s.closeOnce.Do(func() { err = s.closer.Close(); s.End(-1) })
	return err
}
func (s *Session) Cursor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor(s.tail)
}
func (s *Session) cursor(offset uint64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s.ID + ":" + strconv.FormatUint(offset, 10)))
}

type ReadResult struct {
	Events   []Event `json:"events,omitempty"`
	Data     []byte  `json:"data"`
	Next     string  `json:"nextCursor"`
	Oldest   string  `json:"oldestCursor"`
	Overflow bool    `json:"overflow"`
	EOF      bool    `json:"eof"`
	ExitCode *int    `json:"exitCode,omitempty"`
}

func (s *Session) Read(ctx context.Context, cursor string, limit int, wait time.Duration) (*ReadResult, *protocol.Error) {
	return s.ReadSource(ctx, cursor, limit, wait, "all")
}
func (s *Session) ReadSource(ctx context.Context, cursor string, limit int, wait time.Duration, source string) (*ReadResult, *protocol.Error) {
	if source != "all" && source != "rx" && source != "tx" {
		return nil, protocol.Fail("INVALID_PARAMS", "source must be all, rx or tx")
	}
	if limit <= 0 || limit > 48<<10 {
		limit = 48 << 10
	}
	s.mu.Lock()
	offset := s.start
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		parts := strings.Split(string(decoded), ":")
		if err != nil || len(parts) != 2 || parts[0] != s.ID {
			s.mu.Unlock()
			return nil, protocol.Fail("INVALID_CURSOR", "cursor belongs to another session or generation")
		}
		offset, err = strconv.ParseUint(parts[1], 10, 64)
		if err != nil || offset > s.tail {
			s.mu.Unlock()
			return nil, protocol.Fail("INVALID_CURSOR", "cursor offset is invalid")
		}
	}
	if offset == s.tail && !s.eof && wait > 0 {
		changed := s.changed
		s.mu.Unlock()
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-changed:
		case <-timer.C:
		case <-ctx.Done():
			return nil, protocol.Fail("CANCELED", ctx.Err().Error())
		}
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	s.last = time.Now()
	overflow := offset < s.start
	if overflow {
		offset = s.start
	}
	// Serial results contain both aggregate data and event provenance. Bound
	// bytes and event count so base64 plus timestamps fit the RPC frame size.
	if s.Kind == "serial" && limit > 24<<10 {
		limit = 24 << 10
	}
	end := offset + uint64(limit)
	tail := s.tail
	if end > tail {
		end = tail
	}
	if s.Kind == "serial" {
		count := 0
		for _, event := range s.events {
			if event.end <= offset {
				continue
			}
			if event.start >= end {
				break
			}
			count++
			if count > 128 {
				end = event.start
				break
			}
		}
	}
	result := &ReadResult{Next: s.cursor(end), Oldest: s.cursor(s.start), Overflow: overflow, EOF: s.eof && end == tail, ExitCode: s.exit}
	if s.Kind == "serial" {
		for _, event := range s.events {
			if event.end <= offset || event.start >= end || (source != "all" && source != event.direction) {
				continue
			}
			a, b := event.start, event.end
			if a < offset {
				a = offset
			}
			if b > end {
				b = end
			}
			data := s.bytes(a, b)
			result.Data = append(result.Data, data...)
			result.Events = append(result.Events, Event{Time: event.time, Direction: event.direction, Data: data, Cursor: s.cursor(b)})
		}
	} else {
		result.Data = s.bytes(offset, end)
	}
	return result, nil
}
func (s *Session) bytes(start, end uint64) []byte {
	b := make([]byte, int(end-start))
	if len(b) == 0 {
		return b
	}
	i := int(start % BufferSize)
	n := copy(b, s.data[i:])
	copy(b[n:], s.data)
	return b
}
func (s *Session) BeginSerial() (func(), *protocol.Error) {
	if !s.operationMu.TryLock() {
		return nil, protocol.Fail("RESOURCE_BUSY", "another serial command is in progress")
	}
	return s.operationMu.Unlock, nil
}
func (s *Session) Input(member string, b []byte) *protocol.Error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	writable, joined := s.members[member]
	eof := s.eof
	s.last = time.Now()
	s.mu.Unlock()
	if (joined && !writable) || (s.Kind != "serial" && !joined) {
		return protocol.Fail("PERMISSION_DENIED", "attach as an input member first")
	}
	if eof {
		return protocol.Fail("SESSION_CLOSED", "session has ended")
	}
	for len(b) > 0 {
		n, err := s.writer.Write(b)
		if n > 0 && s.Kind == "serial" {
			s.append(b[:n], "tx")
		}
		if err != nil {
			return protocol.Fail("IO_ERROR", err.Error())
		}
		if n == 0 {
			return protocol.Fail("IO_ERROR", "short write")
		}
		b = b[n:]
	}
	return nil
}
func (s *Session) Attach(member string, readOnly bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[member] = !readOnly
	s.last = time.Now()
}
func (s *Session) Detach(member string, closeLast bool) {
	s.mu.Lock()
	_, exists := s.members[member]
	delete(s.members, member)
	last := len(s.members) == 0
	s.mu.Unlock()
	if exists && last && closeLast && s.Kind != "serial" {
		_ = s.Close()
	}
}
func (s *Session) Resize(member string, rows, cols uint16) *protocol.Error {
	s.mu.Lock()
	writable := s.members[member]
	s.mu.Unlock()
	if !writable {
		return protocol.Fail("PERMISSION_DENIED", "input membership is required")
	}
	if rows == 0 || cols == 0 || rows > 1000 || cols > 1000 {
		return protocol.Fail("INVALID_PARAMS", "invalid terminal size")
	}
	if s.resize == nil {
		return protocol.Fail("UNSUPPORTED", "session has no terminal")
	}
	if err := s.resize(rows, cols); err != nil {
		return protocol.Fail("IO_ERROR", err.Error())
	}
	return nil
}
func (s *Session) Status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{"id": s.ID, "kind": s.Kind, "name": s.Name, "createdAt": s.Created, "members": len(s.members), "eof": s.eof, "exitCode": s.exit}
}

type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	ports    map[string]string
}

func NewManager() *Manager {
	return &Manager{sessions: map[string]*Session{}, ports: map[string]string{}}
}
func (m *Manager) Add(s *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sessions) >= 64 {
		return fmt.Errorf("session limit reached")
	}
	if s.Kind == "serial" {
		if _, ok := m.ports[s.Name]; ok {
			return fmt.Errorf("serial port is already open")
		}
		m.ports[s.Name] = s.ID
	}
	m.sessions[s.ID] = s
	return nil
}
func (m *Manager) Get(id string) (*Session, *protocol.Error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil {
		return nil, protocol.Fail("SESSION_NOT_FOUND", "session not found")
	}
	return s, nil
}
func (m *Manager) List() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]map[string]any, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, s.Status())
	}
	return list
}
func (m *Manager) Close(id string) *protocol.Error {
	m.mu.Lock()
	s := m.sessions[id]
	if s == nil {
		m.mu.Unlock()
		return protocol.Fail("SESSION_NOT_FOUND", "session not found")
	}
	delete(m.sessions, id)
	if s.Kind == "serial" {
		delete(m.ports, s.Name)
	}
	m.mu.Unlock()
	if err := s.Close(); err != nil {
		return protocol.Fail("IO_ERROR", err.Error())
	}
	return nil
}
func (m *Manager) Detach(member string) {
	m.mu.Lock()
	var list []*Session
	for _, s := range m.sessions {
		list = append(list, s)
	}
	m.mu.Unlock()
	for _, s := range list {
		s.mu.Lock()
		var members []string
		for id := range s.members {
			if id == member || strings.HasPrefix(id, member+"/") {
				members = append(members, id)
			}
		}
		s.mu.Unlock()
		for _, id := range members {
			s.Detach(id, true)
		}
	}
}
func (m *Manager) CloseAll() {
	m.mu.Lock()
	var ids []string
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Close(id)
	}
}
func (m *Manager) Reap() {
	m.mu.Lock()
	var ids []string
	for id, s := range m.sessions {
		s.mu.Lock()
		expired := len(s.members) == 0 && time.Since(s.last) > 30*time.Minute
		s.mu.Unlock()
		if expired {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Close(id)
	}
}
