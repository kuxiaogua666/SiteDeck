package app

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"sitedeck/internal/client"
	"sitedeck/internal/identity"
	"sitedeck/internal/protocol"
)

type descriptor struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}
type daemonClient struct {
	descriptor
	http     *http.Client
	clientID string
}

func newDaemonClient(path string) (*daemonClient, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d descriptor
	if err = json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	u, err := url.Parse(d.URL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Path != "" {
		return nil, fmt.Errorf("invalid daemon URL")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || len(d.Token) != 64 {
		return nil, fmt.Errorf("invalid local daemon descriptor")
	}
	return &daemonClient{descriptor: d, http: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Minute}, clientID: identity.Random(16)}, nil
}
func (c *daemonClient) request(ctx context.Context, path string, input any) (json.RawMessage, error) {
	b, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("daemon returned %s", resp.Status)
	}
	b, err = io.ReadAll(io.LimitReader(resp.Body, 16<<20+1))
	if len(b) > 16<<20 {
		return nil, fmt.Errorf("daemon response too large")
	}
	return b, err
}
func (c *daemonClient) Call(ctx context.Context, method string, params any) (json.RawMessage, *protocol.Error) {
	timeout := 30000
	if d, ok := ctx.Deadline(); ok {
		timeout = int(time.Until(d).Milliseconds())
	}
	b, err := c.request(ctx, "/rpc", map[string]any{"method": method, "params": params, "timeoutMs": timeout, "clientId": c.clientID})
	if err != nil {
		return nil, protocol.Fail("DAEMON_UNAVAILABLE", err.Error())
	}
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  *protocol.Error `json:"error"`
	}
	if err = json.Unmarshal(b, &response); err != nil {
		return nil, protocol.Fail("DAEMON_ERROR", err.Error())
	}
	return response.Result, response.Error
}
func daemonControl(ctx context.Context, path, action string) (json.RawMessage, error) {
	c, err := newDaemonClient(path)
	if err != nil {
		return nil, err
	}
	return c.request(ctx, "/"+action, map[string]any{})
}

func serveDaemon(ctx context.Context, cfg client.Config, path string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, err := client.Open(ctx, cfg)
	if err != nil {
		return err
	}
	var mu sync.RWMutex
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		if conn != nil {
			conn.Close()
		}
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	d := descriptor{URL: "http://" + listener.Addr().String(), Token: identity.Random(32)}
	b, _ := json.Marshal(d)
	if err = writePrivate(path, append(b, '\n')); err != nil {
		return err
	}
	defer os.Remove(path)
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		c := conn
		var status any = map[string]any{"connected": false}
		if c != nil {
			statusMap := c.Peer.Status()
			statusMap["connected"] = true
			status = statusMap
		}
		mu.RUnlock()
		_ = json.NewEncoder(w).Encode(status)
	})
	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"stopping": true})
		cancel()
	})
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.Request
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, protocol.MaxFrame))
		if err := dec.Decode(&req); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		mu.RLock()
		c := conn
		mu.RUnlock()
		if c == nil {
			_ = json.NewEncoder(w).Encode(protocol.Response{Version: 1, Error: protocol.Fail("RECONNECTING", "daemon is reconnecting; no command was sent")})
			return
		}
		timeout := time.Duration(req.TimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		if timeout > 5*time.Minute {
			timeout = 5 * time.Minute
		}
		callCtx, callCancel := context.WithTimeout(r.Context(), timeout)
		defer callCancel()
		result, rpcErr := c.RPC.CallAs(callCtx, req.Method, req.Params, req.ClientID)
		_ = json.NewEncoder(w).Encode(protocol.Response{Version: 1, Result: result, Error: rpcErr})
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+d.Token)) != 1 {
			http.Error(w, "forbidden", 403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	go func() {
		<-ctx.Done()
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(stopCtx)
	}()
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			mu.RLock()
			c := conn
			mu.RUnlock()
			if c != nil {
				select {
				case <-c.RPC.Done():
				case <-c.Peer.Context().Done():
				default:
					continue
				}
				mu.Lock()
				if conn == c {
					conn = nil
				}
				mu.Unlock()
				c.Close()
			}
			next, err := client.Open(ctx, cfg)
			if err != nil {
				log.Printf("daemon reconnect failed: %v", err)
				continue
			}
			mu.Lock()
			if ctx.Err() != nil {
				mu.Unlock()
				next.Close()
				return
			}
			conn = next
			mu.Unlock()
			log.Print("daemon connection restored")
		}
	}()
	emit(map[string]any{"ready": true, "descriptorFile": path, "deviceId": cfg.Device})
	err = server.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
