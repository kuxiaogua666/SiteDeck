package client

import (
	"context"
	"fmt"
	"time"

	"github.com/pion/webrtc/v4"
	"sitedeck/internal/protocol"
	"sitedeck/internal/rpc"
	"sitedeck/internal/transport"
)

type Config struct {
	Hub         string             `json:"hub"`
	Device      string             `json:"deviceId"`
	Fingerprint string             `json:"fingerprint"`
	Token       string             `json:"token"`
	AccessToken string             `json:"accessToken,omitempty"`
	Proxy       string             `json:"proxy,omitempty"`
	Transport   string             `json:"transport"`
	ICE         []webrtc.ICEServer `json:"ice,omitempty"`
	ICETimeout  time.Duration      `json:"-"`
}
type Connection struct {
	RPC           *rpc.Client
	Peer          *transport.Peer
	Socket        *transport.Socket
	cancel        context.CancelFunc
	Establishment time.Duration
}

func Open(ctx context.Context, cfg Config) (*Connection, error) {
	started := time.Now()
	ctx, cancel := context.WithCancel(ctx)
	fail := func(err error) (*Connection, error) { cancel(); return nil, err }
	if cfg.Device == "" || cfg.Token == "" || cfg.Fingerprint == "" {
		return fail(fmt.Errorf("device, guest token and Agent fingerprint are required"))
	}
	dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	socket, err := transport.DialWithProxy(dialCtx, cfg.Hub, "", cfg.Proxy)
	dialCancel()
	if err != nil {
		return fail(err)
	}
	go func() { <-ctx.Done(); socket.Close() }()
	if err = socket.Send(protocol.Signal{Type: "connect", Device: cfg.Device, AccessToken: cfg.AccessToken}); err != nil {
		socket.Close()
		return fail(err)
	}
	_ = socket.Conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	msg, err := socket.Read()
	_ = socket.Conn.SetReadDeadline(time.Time{})
	if err != nil {
		socket.Close()
		return fail(err)
	}
	if msg.Type != "connected" {
		socket.Close()
		return fail(protocol.Fail(msg.Reason, "Hub rejected the connection"))
	}
	peer := transport.NewPeer(ctx, socket, msg.Channel, cfg.ICE)
	go func() { <-ctx.Done(); peer.Close(); socket.Close() }()
	go func() {
		defer cancel()
		for {
			_ = socket.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			msg, err := socket.Read()
			if err != nil {
				return
			}
			if msg.Channel == peer.Channel {
				peer.Handle(msg)
			}
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
				if peer.Send(protocol.Signal{Type: "heartbeat"}) != nil {
					cancel()
					return
				}
			}
		}
	}()
	if cfg.Transport == "" {
		cfg.Transport = "auto"
	}
	if cfg.ICETimeout <= 0 {
		cfg.ICETimeout = 8 * time.Second
	}
	connectCtx, connectCancel := context.WithTimeout(ctx, 2*cfg.ICETimeout+20*time.Second)
	defer connectCancel()
	stream, err := peer.Connect(connectCtx, cfg.Transport, cfg.ICETimeout)
	if err != nil {
		return fail(err)
	}
	secure, err := transport.SecureClient(connectCtx, stream, cfg.Fingerprint, cfg.Token, cfg.Device, msg.Channel)
	if err != nil {
		stream.Close()
		return fail(protocol.Fail("CHANNEL_AUTH_FAILED", err.Error()))
	}
	return &Connection{RPC: rpc.NewClient(secure), Peer: peer, Socket: socket, cancel: cancel, Establishment: time.Since(started)}, nil
}
func (c *Connection) Close() { c.cancel(); c.RPC.Close(); c.Peer.Close(); c.Socket.Close() }
