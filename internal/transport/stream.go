package transport

import (
	"context"
	"io"
	"net"
	"sync"
	"time"
)

// Stream adapts reliable, ordered message transports to a bounded byte stream.
// The reader never silently drops a frame: overflowing the queue closes it.
type Stream struct {
	mu            sync.Mutex
	writeMu       sync.Mutex
	readMu        sync.Mutex
	queue         chan []byte
	done          chan struct{}
	once          sync.Once
	pending       []byte
	send          func([]byte) error
	readDeadline  time.Time
	writeDeadline time.Time
}

func NewStream(send func([]byte) error) *Stream {
	return &Stream{queue: make(chan []byte, 256), done: make(chan struct{}), send: send}
}
func (s *Stream) Feed(b []byte) {
	copyB := append([]byte(nil), b...)
	select {
	case <-s.done:
		return
	default:
	}
	select {
	case s.queue <- copyB:
	case <-s.done:
	default:
		s.Close()
	}
}
func (s *Stream) Read(b []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if len(b) == 0 {
		return 0, nil
	}
	if len(s.pending) == 0 {
		s.mu.Lock()
		deadline := s.readDeadline
		s.mu.Unlock()
		var timer <-chan time.Time
		var t *time.Timer
		if !deadline.IsZero() {
			t = time.NewTimer(time.Until(deadline))
			timer = t.C
			defer t.Stop()
		}
		select {
		case s.pending = <-s.queue:
		case <-s.done:
			return 0, io.EOF
		case <-timer:
			return 0, context.DeadlineExceeded
		}
	}
	n := copy(b, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}
func (s *Stream) Write(b []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n := 0
	for len(b) > 0 {
		select {
		case <-s.done:
			return n, io.ErrClosedPipe
		default:
		}
		s.mu.Lock()
		deadline := s.writeDeadline
		s.mu.Unlock()
		if !deadline.IsZero() && time.Now().After(deadline) {
			return n, context.DeadlineExceeded
		}
		k := len(b)
		if k > 12<<10 {
			k = 12 << 10
		}
		if err := s.send(b[:k]); err != nil {
			return n, err
		}
		n += k
		b = b[k:]
	}
	return n, nil
}
func (s *Stream) Close() error { s.once.Do(func() { close(s.done) }); return nil }

type address string

func (a address) Network() string      { return "sitedeck" }
func (a address) String() string       { return string(a) }
func (s *Stream) LocalAddr() net.Addr  { return address("local") }
func (s *Stream) RemoteAddr() net.Addr { return address("peer") }
func (s *Stream) SetDeadline(t time.Time) error {
	s.mu.Lock()
	s.readDeadline = t
	s.writeDeadline = t
	s.mu.Unlock()
	return nil
}
func (s *Stream) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	s.readDeadline = t
	s.mu.Unlock()
	return nil
}
func (s *Stream) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	s.writeDeadline = t
	s.mu.Unlock()
	return nil
}
