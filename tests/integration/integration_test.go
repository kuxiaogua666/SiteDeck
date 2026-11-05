//go:build !windows

package integration

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
	"sitedeck/internal/agent"
	"sitedeck/internal/client"
	"sitedeck/internal/hub"
	"sitedeck/internal/identity"
	"sitedeck/internal/protocol"
	"sitedeck/internal/session"
)

type site struct {
	cfg   client.Config
	ctx   context.Context
	state *identity.State
}

func start(t *testing.T, allow string, dropOffers ...bool) *site {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	state, err := identity.Init(t.TempDir() + "/identity.json")
	if err != nil {
		t.Fatal(err)
	}
	registration := identity.Random(32)
	server := httptest.NewServer(hub.New(registration).Handler())
	hubURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	if len(dropOffers) > 0 && dropOffers[0] {
		proxy := signalingProxy(t, hubURL)
		hubURL = "ws" + strings.TrimPrefix(proxy.URL, "http") + "/ws"
	}
	done := make(chan error, 1)
	go func() {
		done <- agent.Run(ctx, agent.Config{Hub: hubURL, State: state, Registration: registration, Allow: allow})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Agent did not stop")
		}
		server.Close()
	})
	s := &site{ctx: ctx, state: state, cfg: client.Config{Hub: hubURL, Device: state.Device, Fingerprint: state.Fingerprint(), Token: state.GuestToken, Transport: "relay", ICETimeout: 2 * time.Second}}
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := client.Open(ctx, s.cfg)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return s
}

func signalingProxy(t *testing.T, upstream string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, _, err := websocket.DefaultDialer.Dial(upstream, http.Header{"Authorization": r.Header.Values("Authorization")})
		if err != nil {
			http.Error(w, "upstream failed", 502)
			return
		}
		defer peer.Close()
		upgrader := websocket.Upgrader{}
		clientConn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer clientConn.Close()
		done := make(chan struct{}, 2)
		copyFrames := func(dst, src *websocket.Conn) {
			defer func() { done <- struct{}{} }()
			for {
				kind, b, err := src.ReadMessage()
				if err != nil {
					return
				}
				var frame protocol.Signal
				_ = json.Unmarshal(b, &frame)
				if frame.Type == "offer" {
					continue
				}
				if dst.WriteMessage(kind, b) != nil {
					return
				}
			}
		}
		go copyFrames(peer, clientConn)
		go copyFrames(clientConn, peer)
		<-done
	}))
	t.Cleanup(server.Close)
	return server
}
func (s *site) open(t *testing.T) *client.Connection {
	t.Helper()
	c, err := client.Open(s.ctx, s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
func call(t *testing.T, c *client.Connection, method string, p any) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := c.RPC.Call(ctx, method, p)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return raw
}
func object(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestAuthenticatedRelayAndHost(t *testing.T) {
	s := start(t, "host")
	c := s.open(t)
	doctor := object(t, call(t, c, "doctor", map[string]any{}))
	if doctor["connection"].(map[string]any)["transport"] != "relay" {
		t.Fatal(doctor)
	}
	dir := t.TempDir()
	out := object(t, call(t, c, "host.exec", map[string]any{"argv": []string{"/bin/sh", "-c", "printf '%s:%s' \"$SITE_TEST\" \"$PWD\"; printf error >&2; exit 7"}, "cwd": dir, "env": map[string]string{"SITE_TEST": "hello"}}))
	if out["stdout"] != "hello:"+dir || out["stderr"] != "error" || out["exitCode"] != float64(7) {
		t.Fatal(out)
	}
	for _, change := range []string{"pin", "token"} {
		cfg := s.cfg
		if change == "pin" {
			cfg.Fingerprint = strings.Repeat("0", 64)
		} else {
			cfg.Token = identity.Random(32)
		}
		ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
		bad, err := client.Open(ctx, cfg)
		cancel()
		if bad != nil {
			bad.Close()
		}
		if err == nil {
			t.Fatalf("invalid %s accepted", change)
		}
	}
	large := object(t, call(t, c, "host.exec", map[string]any{"command": "head -c 65536 /dev/zero | tr '\\000' x"}))
	artifact := large["artifact"].(map[string]any)
	id := artifact["id"].(string)
	offset := 0
	var all []byte
	for {
		raw := call(t, c, "artifact.read", map[string]any{"id": id, "offset": offset})
		var r struct {
			Data []byte `json:"data"`
			Next int    `json:"nextOffset"`
			EOF  bool   `json:"eof"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		all = append(all, r.Data...)
		offset = r.Next
		if r.EOF {
			break
		}
	}
	full := object(t, all)
	if len(full["stdout"].(string)) != 65536 {
		t.Fatal("artifact lost output")
	}
	// Invalid UTF-8 must retain the original bytes.
	invalid := object(t, call(t, c, "host.exec", map[string]any{"command": "printf '\\377'"}))
	if invalid["rawStdout"] != "/w==" {
		t.Fatal(invalid)
	}
}

func TestLocalP2P(t *testing.T) {
	s := start(t, "host")
	cfg := s.cfg
	cfg.Transport = "p2p"
	cfg.ICETimeout = 5 * time.Second
	c, err := client.Open(s.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := object(t, call(t, c, "doctor", map[string]any{}))
	connection := r["connection"].(map[string]any)
	if connection["transport"] != "p2p" || connection["candidateTypeLocal"] != "host" {
		t.Fatal(connection)
	}
	if r := object(t, call(t, c, "host.exec", map[string]any{"argv": []string{"/bin/echo", "p2p-ok"}})); r["stdout"] != "p2p-ok\n" {
		t.Fatal(r)
	}
}

func TestAutomaticRelayFallback(t *testing.T) {
	s := start(t, "host", true)
	cfg := s.cfg
	cfg.Transport = "auto"
	cfg.ICETimeout = time.Second
	c, err := client.Open(s.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Peer.Status()["transport"] != "relay" || c.Peer.Status()["fallbackReason"] != "P2P_FAILED" {
		t.Fatal(c.Peer.Status())
	}
	if r := object(t, call(t, c, "host.exec", map[string]any{"command": "printf fallback-ok"})); r["stdout"] != "fallback-ok" {
		t.Fatal(r)
	}
}

func TestPermissionAndConcurrentCancellation(t *testing.T) {
	s := start(t, "")
	c := s.open(t)
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
	defer cancel()
	_, err := c.RPC.Call(ctx, "host.exec", map[string]any{"command": "true"})
	if err == nil || err.Code != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	full := start(t, "host")
	active := full.open(t)
	short, stop := context.WithCancel(full.ctx)
	defer stop()
	time.AfterFunc(150*time.Millisecond, stop)
	_, err = active.RPC.Call(short, "host.exec", map[string]any{"command": "sleep 30"})
	if err == nil || err.Code != "CANCELED" {
		t.Fatalf("expected cancellation: %v", err)
	}
	call(t, active, "doctor", map[string]any{})
	if r := object(t, call(t, active, "host.exec", map[string]any{"command": "printf after-cancel"})); r["stdout"] != "after-cancel" {
		t.Fatal(r)
	}
}

func TestSharedSessionAndSerialCursors(t *testing.T) {
	s := start(t, "host,serial")
	a, b := s.open(t), s.open(t)
	opened := object(t, call(t, a, "host.shell", map[string]any{"command": "cat"}))
	id := opened["id"].(string)
	call(t, a, "session.attach", map[string]any{"id": id})
	call(t, b, "session.attach", map[string]any{"id": id, "readOnly": true})
	call(t, a, "session.write", map[string]any{"id": id, "text": "shared-output\n"})
	var read session.ReadResult
	if err := json.Unmarshal(call(t, b, "session.read", map[string]any{"id": id, "waitMs": 1000}), &read); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(read.Data), "shared-output") {
		t.Fatalf("missing shared output %q", read.Data)
	}
	ctx, cancel := context.WithTimeout(s.ctx, time.Second)
	defer cancel()
	_, rpcErr := b.RPC.Call(ctx, "session.write", map[string]any{"id": id, "text": "must-not-write"})
	if rpcErr == nil || rpcErr.Code != "PERMISSION_DENIED" {
		t.Fatal(rpcErr)
	}
	call(t, a, "session.detach", map[string]any{"id": id})
	call(t, b, "session.detach", map[string]any{"id": id})
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	name := slave.Name()
	slave.Close()
	serialSession := object(t, call(t, a, "serial.connect", map[string]any{"port": name, "baudRate": 115200}))
	sid := serialSession["id"].(string)
	if _, err := a.RPC.Call(ctx, "serial.connect", map[string]any{"port": name}); err == nil || err.Code != "RESOURCE_BUSY" {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 256)
		n, err := master.Read(buf)
		if err == nil && strings.Contains(string(buf[:n]), "ping") {
			_, _ = master.Write([]byte("pong> "))
		}
	}()
	exec := object(t, call(t, a, "serial.exec", map[string]any{"id": sid, "text": "ping\n", "pattern": "> "}))
	if exec["matched"] != true {
		t.Fatal(exec)
	}
	var history session.ReadResult
	if err := json.Unmarshal(call(t, a, "serial.read", map[string]any{"id": sid}), &history); err != nil {
		t.Fatal(err)
	}
	directions := map[string]bool{}
	for _, event := range history.Events {
		directions[event.Direction] = true
	}
	if !directions["tx"] || !directions["rx"] {
		t.Fatalf("missing TX/RX provenance: %+v", history.Events)
	}
	var rx session.ReadResult
	if err := json.Unmarshal(call(t, a, "serial.read", map[string]any{"id": sid, "source": "rx"}), &rx); err != nil {
		t.Fatal(err)
	}
	if string(rx.Data) != "pong> " {
		t.Fatalf("TX bytes leaked into RX view: %q", rx.Data)
	}
	_, rpcErr = a.RPC.Call(ctx, "serial.read", map[string]any{"id": sid, "cursor": read.Next})
	if rpcErr == nil || rpcErr.Code != "INVALID_CURSOR" {
		t.Fatal(rpcErr)
	}
	go func() {
		payload := bytes.Repeat([]byte("z"), session.BufferSize+4096)
		payload = append(payload, []byte("OVERFLOW_END")...)
		_, _ = master.Write(payload)
	}()
	call(t, a, "serial.wait", map[string]any{"id": sid, "cursor": history.Next, "pattern": "OVERFLOW_END"})
	var overflow session.ReadResult
	if err := json.Unmarshal(call(t, a, "serial.read", map[string]any{"id": sid, "cursor": history.Next}), &overflow); err != nil {
		t.Fatal(err)
	}
	if !overflow.Overflow || overflow.Oldest == history.Oldest {
		t.Fatal("old cursor did not report overflow")
	}
	call(t, a, "serial.disconnect", map[string]any{"id": sid})
}

func TestMultiplexedSessionMembership(t *testing.T) {
	s := start(t, "host")
	c := s.open(t)
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	invoke := func(actor, method string, p any) json.RawMessage {
		t.Helper()
		r, err := c.RPC.CallAs(ctx, method, p, actor)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	opened := object(t, invoke("first", "host.shell", map[string]any{"command": "cat"}))
	id := opened["id"].(string)
	invoke("first", "session.attach", map[string]any{"id": id})
	invoke("second", "session.attach", map[string]any{"id": id})
	invoke("first", "session.detach", map[string]any{"id": id})
	invoke("second", "session.write", map[string]any{"id": id, "text": "still-attached\n"})
	r := object(t, invoke("second", "session.attach", map[string]any{"id": id}))
	if r["members"] != float64(1) || r["eof"] != false {
		t.Fatal(r)
	}
	invoke("second", "session.detach", map[string]any{"id": id})
}

func sshServer(t *testing.T) (string, int, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if meta.User() == "tester" && string(password) == "test-password" {
			return nil, nil
		}
		return nil, fmt.Errorf("denied")
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					conn.Close()
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for next := range channels {
					if next.ChannelType() != "session" {
						next.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					ch, reqs, err := next.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer ch.Close()
						for req := range reqs {
							if req.Type == "exec" {
								_ = req.Reply(true, nil)
								_, _ = io.WriteString(ch, "ssh-ok")
								_, _ = io.WriteString(ch.Stderr(), "ssh-stderr")
								_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{7}))
								return
							}
							_ = req.Reply(false, nil)
						}
					}()
				}
			}()
		}
	}()
	addr := listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, ssh.FingerprintSHA256(signer.PublicKey())
}
func TestSSHFromAgent(t *testing.T) {
	s := start(t, "ssh")
	c := s.open(t)
	host, port, pin := sshServer(t)
	scan := object(t, call(t, c, "ssh.hostkey.scan", map[string]any{"host": host, "port": port}))
	if scan["fingerprint"] != pin || scan["verified"] != false {
		t.Fatal(scan)
	}
	p := map[string]any{"host": host, "port": port, "user": "tester", "password": "test-password", "fingerprint": pin, "command": "anything"}
	r := object(t, call(t, c, "ssh.exec", p))
	if r["stdout"] != "ssh-ok" || r["stderr"] != "ssh-stderr" || r["exitCode"] != float64(7) {
		t.Fatal(r)
	}
	p["fingerprint"] = "SHA256:wrong"
	ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()
	_, err := c.RPC.Call(ctx, "ssh.exec", p)
	if err == nil || !strings.Contains(err.Message, "SSH_HOST_KEY_MISMATCH") {
		t.Fatal(err)
	}
}

func TestSSHHandshakeCancellation(t *testing.T) {
	s := start(t, "ssh")
	c := s.open(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	closed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, _ = io.Copy(io.Discard, conn)
		close(closed)
	}()
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	time.AfterFunc(150*time.Millisecond, cancel)
	_, rpcErr := c.RPC.Call(ctx, "ssh.hostkey.scan", map[string]any{"host": "127.0.0.1", "port": listener.Addr().(*net.TCPAddr).Port})
	if rpcErr == nil || rpcErr.Code != "CANCELED" {
		t.Fatal(rpcErr)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("canceled SSH handshake retained its TCP connection")
	}
}
