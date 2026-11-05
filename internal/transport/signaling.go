package transport

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"sitedeck/internal/protocol"
)

type Socket struct {
	Conn *websocket.Conn
	mu   sync.Mutex
}

func Wrap(c *websocket.Conn) *Socket { c.SetReadLimit(protocol.MaxFrame); return &Socket{Conn: c} }
func (s *Socket) Send(m protocol.Signal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return s.Conn.WriteJSON(m)
}
func (s *Socket) Read() (protocol.Signal, error) {
	var m protocol.Signal
	err := s.Conn.ReadJSON(&m)
	return m, err
}
func (s *Socket) Close() error { return s.Conn.Close() }
func ValidateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid Hub URL")
	}
	if u.Scheme != "wss" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if u.Scheme != "ws" || ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("remote Hub requires wss://; ws:// is restricted to loopback IPs")
		}
	}
	return nil
}
func Dial(ctx context.Context, endpoint, registration string) (*Socket, error) {
	return DialWithProxy(ctx, endpoint, registration, "")
}

// DialWithProxy selects environment proxies by default, or explicitly bypasses them.
func DialWithProxy(ctx context.Context, endpoint, registration, proxyMode string) (*Socket, error) {
	if err := ValidateEndpoint(endpoint); err != nil {
		return nil, err
	}
	dialer := *websocket.DefaultDialer
	switch proxyMode {
	case "", "env":
	case "direct":
		dialer.Proxy = nil
	default:
		return nil, fmt.Errorf("proxy must be env or direct")
	}
	usedProxy := false
	if selectProxy := dialer.Proxy; selectProxy != nil {
		dialer.Proxy = func(req *http.Request) (*url.URL, error) {
			u, err := selectProxy(req)
			usedProxy = u != nil
			return u, err
		}
	}
	head := http.Header{}
	if registration != "" {
		head.Set("Authorization", "Bearer "+registration)
	}
	c, resp, err := dialer.DialContext(ctx, endpoint, head)
	if err != nil {
		if usedProxy {
			return nil, fmt.Errorf("Hub connection through environment proxy failed; use --proxy direct if this Hub should be reached directly")
		}
		if resp != nil {
			return nil, fmt.Errorf("Hub handshake: %s", resp.Status)
		}
		return nil, err
	}
	return Wrap(c), nil
}
