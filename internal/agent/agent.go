package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"sitedeck/internal/access"
	"sitedeck/internal/identity"
	"sitedeck/internal/protocol"
	"sitedeck/internal/transport"
)

type Config struct {
	Hub             string
	Registration    string
	State           *identity.State
	Allow           string
	ICE             []webrtc.ICEServer
	LocalOnly       bool
	Observer        func(Event)
	OperationsReady func(*Operations)
}

func Run(ctx context.Context, cfg Config) error {
	ops := NewOperations(cfg.State, cfg.Allow)
	defer ops.Sessions.CloseAll()
	if cfg.OperationsReady != nil {
		cfg.OperationsReady(ops)
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ops.Sessions.Reap()
			}
		}
	}()
	if cfg.LocalOnly {
		cfg.notify(Event{Type: "state", State: "local", Message: "本机模式已启动，未启用远程接入"})
		<-ctx.Done()
		return nil
	}
	delay := time.Second
	for ctx.Err() == nil {
		cfg.notify(Event{Type: "state", State: "connecting", Message: "正在连接 Hub"})
		start := time.Now()
		err := connect(ctx, cfg, ops)
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("Hub disconnected: %v; retrying", err)
		cfg.notify(Event{Type: "state", State: "reconnecting", Message: fmt.Sprint(err)})
		if time.Since(start) > time.Minute {
			delay = time.Second
		}
		jitter := time.Duration(time.Now().UnixNano() % int64(delay/2+1))
		timer := time.NewTimer(delay + jitter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if delay < 20*time.Second {
			delay *= 2
		}
	}
	return nil
}
func connect(ctx context.Context, cfg Config, ops *Operations) error {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	socket, err := transport.Dial(dialCtx, cfg.Hub, cfg.Registration)
	cancel()
	if err != nil {
		return err
	}
	defer socket.Close()
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()
	go func() { <-ctx.Done(); socket.Close() }()
	name := cfg.State.Name
	if name == "" {
		name, _ = os.Hostname()
	}
	var capabilities []string
	for _, scope := range []string{"host", "ssh", "serial"} {
		if ops.Allowed[scope] {
			capabilities = append(capabilities, scope)
		}
	}
	if err = socket.Send(protocol.Signal{Type: "register", Device: cfg.State.Device, Name: name, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: protocol.Version, Capabilities: capabilities, AccessProtocol: 1}); err != nil {
		return err
	}
	_ = socket.Conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	msg, err := socket.Read()
	if err != nil {
		return err
	}
	if msg.Type != "registered" {
		return fmt.Errorf("registration rejected: %s", msg.Reason)
	}
	log.Printf("registered device=%s permissions=%v", cfg.State.Device, cfg.Allow)
	cfg.notify(Event{Type: "state", State: "online", Message: "已注册到 Hub，等待授权连接"})
	signingKey := msg.SigningKey
	var mu sync.Mutex
	peers := map[string]*transport.Peer{}
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, p := range peers {
			p.Close()
		}
	}()
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if socket.Send(protocol.Signal{Type: "heartbeat"}) != nil {
					socket.Close()
					return
				}
			}
		}
	}()
	for {
		_ = socket.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		msg, err = socket.Read()
		if err != nil {
			return err
		}
		if msg.Type == "connected" {
			var claims *access.Claims
			peerCtx := ctx
			cancelPeer := func() {}
			if msg.AccessRequired || msg.AccessTicket != "" {
				claims, err = access.Verify(signingKey, msg.AccessTicket, cfg.State.Device, msg.Channel, time.Now())
				if err != nil {
					_ = socket.Send(protocol.Signal{Type: "close", Channel: msg.Channel, Reason: "INVALID_ACCESS_TICKET"})
					continue
				}
				peerCtx, cancelPeer = context.WithDeadline(ctx, time.UnixMilli(claims.ExpiresAtMS))
			}
			p := transport.NewPeer(peerCtx, socket, msg.Channel, cfg.ICE)
			mu.Lock()
			peers[msg.Channel] = p
			mu.Unlock()
			go func() {
				defer cancelPeer()
				for {
					select {
					case <-p.Context().Done():
						cfg.notify(Event{Type: "channel.closed", Channel: p.Channel, Message: "连接已关闭"})
						_ = p.Send(protocol.Signal{Type: "close", Reason: "CHANNEL_CLOSED"})
						return
					case ready := <-p.Ready():
						go func() { serve(p.Context(), ready.Conn, cfg.State, p, ops, claims, cfg.Observer) }()
					}
				}
			}()
			continue
		}
		mu.Lock()
		p := peers[msg.Channel]
		if msg.Type == "close" {
			delete(peers, msg.Channel)
		}
		mu.Unlock()
		if p == nil {
			continue
		}
		if msg.Type == "offer" {
			offer := msg
			p.Expect(offer.Attempt)
			go func() {
				attempt, cancel := context.WithTimeout(p.Context(), 15*time.Second)
				defer cancel()
				if err := p.AcceptOffer(attempt, offer); err != nil {
					log.Printf("ICE answer failed channel=%s: %v", p.Channel, err)
				}
			}()
		} else {
			p.Handle(msg)
		}
	}
}

func serve(ctx context.Context, raw net.Conn, state *identity.State, peer *transport.Peer, ops *Operations, claims *access.Claims, observer func(Event)) {
	defer raw.Close()
	authCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, err := transport.SecureServer(authCtx, raw, state, peer.Channel)
	cancel()
	if err != nil {
		log.Printf("channel authentication failed channel=%s", peer.Channel)
		if peer.IsCurrent(raw) {
			_ = peer.Send(protocol.Signal{Type: "close", Reason: "AUTH_FAILED"})
			peer.Close()
		}
		return
	}
	var scopes map[string]bool
	if claims != nil {
		scopes = map[string]bool{}
		for _, scope := range claims.Scopes {
			scopes[scope] = true
		}
	}
	allowed := ops.effective(scopes)
	status := peer.Status()
	kind, _ := status["transport"].(string)
	_ = peer.Send(protocol.Signal{Type: "channel.ready", Transport: kind})
	if observer != nil {
		observer(Event{Time: time.Now().UTC(), Type: "channel.ready", Channel: peer.Channel, Transport: kind, Message: "远程通道已通过认证"})
	}
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()
	defer ops.Sessions.Detach(peer.Channel)
	go func() { <-ctx.Done(); raw.Close() }()
	var writeMu sync.Mutex
	respond := func(r protocol.Response) {
		writeMu.Lock()
		defer writeMu.Unlock()
		if json.NewEncoder(conn).Encode(r) != nil {
			cancel()
		}
	}
	var mu sync.Mutex
	type execution struct {
		cancel   context.CancelFunc
		clientID string
	}
	active := map[string]execution{}
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, entry := range active {
			entry.cancel()
		}
	}()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 8192), protocol.MaxFrame)
	for scanner.Scan() {
		var req protocol.Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			respond(protocol.Response{Version: 1, Error: protocol.Fail("INVALID_REQUEST", "malformed JSON")})
			continue
		}
		if req.Version != 1 || req.ID == "" || len(req.ID) > 128 || len(req.ClientID) > 64 || len(req.Method) > 64 {
			respond(protocol.Response{Version: 1, ID: req.ID, Error: protocol.Fail("INVALID_REQUEST", "protocol version 1 and request id are required")})
			continue
		}
		if req.Method == "request.cancel" {
			var target struct {
				ID string `json:"id"`
			}
			rpcErr := protocol.Decode(req.Params, &target)
			if rpcErr != nil {
				respond(protocol.Response{Version: 1, ID: req.ID, Error: rpcErr})
				continue
			}
			mu.Lock()
			entry, exists := active[target.ID]
			owned := exists && entry.clientID == req.ClientID
			if owned {
				entry.cancel()
			}
			mu.Unlock()
			respond(protocol.Response{Version: 1, ID: req.ID, Result: map[string]bool{"canceled": owned}})
			continue
		}
		mu.Lock()
		_, duplicate := active[req.ID]
		if duplicate || len(active) >= 8 {
			mu.Unlock()
			respond(protocol.Response{Version: 1, ID: req.ID, Error: protocol.Fail("RESOURCE_BUSY", "duplicate id or too many concurrent requests")})
			continue
		}
		timeout := time.Duration(req.TimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		if timeout > 5*time.Minute {
			timeout = 5 * time.Minute
		}
		requestCtx, requestCancel := context.WithTimeout(ctx, timeout)
		active[req.ID] = execution{cancel: requestCancel, clientID: req.ClientID}
		mu.Unlock()
		go func(req protocol.Request) {
			defer requestCancel()
			defer func() { mu.Lock(); delete(active, req.ID); mu.Unlock() }()
			member := peer.Channel
			if req.ClientID != "" {
				member += "/" + req.ClientID
			}
			connection := peer.Status()
			if claims != nil {
				connection["grantId"] = claims.Grant
				connection["accessExpiresAt"] = time.UnixMilli(claims.ExpiresAtMS)
			}
			result, rpcErr := ops.Handle(requestCtx, member, connection, req, allowed)
			code := "OK"
			if rpcErr != nil {
				code = rpcErr.Code
			}
			log.Printf("rpc device=%s channel=%s request=%q method=%q result=%s", state.Device, peer.Channel, req.ID, req.Method, code)
			respond(protocol.Response{Version: 1, ID: req.ID, Result: result, Error: rpcErr})
		}(req)
	}
}
