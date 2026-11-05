package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestDialProxySelection(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"test"}`))
	}))
	defer upstream.Close()
	var hits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		c, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			c.Close()
		}
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	previous := websocket.DefaultDialer
	clone := *previous
	clone.TLSClientConfig = &tls.Config{RootCAs: roots}
	clone.Proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	websocket.DefaultDialer = &clone
	t.Cleanup(func() { websocket.DefaultDialer = previous })
	endpoint := "wss" + strings.TrimPrefix(upstream.URL, "https")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Dial(ctx, endpoint, ""); err == nil || !strings.Contains(err.Error(), "--proxy direct") {
		t.Fatalf("expected actionable proxy failure, got %v", err)
	}
	before := hits.Load()
	if before != 1 {
		t.Fatalf("proxy hits: %d", before)
	}
	socket, err := DialWithProxy(ctx, endpoint, "", "direct")
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	packet, err := socket.Read()
	if err != nil || packet.Type != "test" {
		t.Fatalf("direct WebSocket failed: %v", err)
	}
	if hits.Load() != before {
		t.Fatal("direct connection used proxy")
	}
	if websocket.DefaultDialer.Proxy == nil {
		t.Fatal("global proxy setting mutated")
	}
	if _, err := DialWithProxy(ctx, endpoint, "", "invalid"); err == nil {
		t.Fatal("invalid mode accepted")
	}
}
