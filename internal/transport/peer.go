package transport

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"sitedeck/internal/identity"
	"sitedeck/internal/protocol"
)

type Ready struct {
	Conn    net.Conn
	Attempt string
}
type Peer struct {
	Socket    *Socket
	Channel   string
	ICE       []webrtc.ICEServer
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	pc        *webrtc.PeerConnection
	stream    *Stream
	attempt   string
	expected  string
	ready     chan Ready
	answers   chan protocol.Signal
	Transport string
	Fallback  string
	Started   time.Time
}

func NewPeer(ctx context.Context, socket *Socket, channel string, ice []webrtc.ICEServer) *Peer {
	ctx, cancel := context.WithCancel(ctx)
	p := &Peer{Socket: socket, Channel: channel, ICE: ice, ctx: ctx, cancel: cancel, ready: make(chan Ready, 4), answers: make(chan protocol.Signal, 4), Started: time.Now()}
	go func() { <-ctx.Done(); p.closeTransport() }()
	return p
}
func (p *Peer) Send(m protocol.Signal) error { m.Channel = p.Channel; return p.Socket.Send(m) }
func (p *Peer) Close()                       { p.cancel() }
func (p *Peer) Expect(attempt string)        { p.mu.Lock(); p.expected = attempt; p.mu.Unlock() }
func (p *Peer) IsCurrent(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stream == conn
}
func (p *Peer) closeTransport() {
	p.mu.Lock()
	pc, s := p.pc, p.stream
	p.pc = nil
	p.stream = nil
	p.attempt = ""
	p.mu.Unlock()
	if s != nil {
		_ = s.Close()
	}
	if pc != nil {
		_ = pc.Close()
	}
}
func (p *Peer) setStream(s *Stream, attempt string) {
	p.mu.Lock()
	if p.attempt != attempt || p.ctx.Err() != nil {
		p.mu.Unlock()
		s.Close()
		return
	}
	old := p.stream
	p.stream = s
	p.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	select {
	case p.ready <- Ready{Conn: s, Attempt: attempt}:
	case <-p.ctx.Done():
		s.Close()
	default:
		s.Close()
	}
}
func (p *Peer) Handle(m protocol.Signal) {
	switch m.Type {
	case "answer":
		select {
		case p.answers <- m:
		default:
		}
	case "relay":
		p.Expect(m.Attempt)
		p.closeTransport()
		p.mu.Lock()
		p.Transport = "relay"
		p.attempt = m.Attempt
		p.mu.Unlock()
		p.setStream(NewStream(func(b []byte) error { return p.Send(protocol.Signal{Type: "data", Data: b, Attempt: m.Attempt}) }), m.Attempt)
	case "data":
		p.mu.Lock()
		s := p.stream
		current := p.attempt == m.Attempt
		p.mu.Unlock()
		if s != nil && current {
			s.Feed(m.Data)
		}
	case "close":
		p.Close()
	}
}
func (p *Peer) connection(ice []webrtc.ICEServer, attempt string) (*webrtc.PeerConnection, error) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.ctx.Err() != nil || p.expected != attempt {
		p.mu.Unlock()
		pc.Close()
		return nil, context.Canceled
	}
	oldPC, oldStream := p.pc, p.stream
	p.pc = pc
	p.stream = nil
	p.attempt = attempt
	p.mu.Unlock()
	if oldStream != nil {
		oldStream.Close()
	}
	if oldPC != nil {
		oldPC.Close()
	}
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			p.mu.Lock()
			s := p.stream
			current := p.pc == pc
			p.mu.Unlock()
			if current && s != nil {
				s.Close()
			}
		}
	})
	return pc, nil
}
func (p *Peer) attach(dc *webrtc.DataChannel, attempt string) {
	s := NewStream(func(b []byte) error {
		// Pion Send queues data. Bound pending bytes to avoid unbounded writers.
		for dc.BufferedAmount() > 1<<20 {
			select {
			case <-p.ctx.Done():
				return p.ctx.Err()
			case <-sDone(dc):
				return fmt.Errorf("data channel closed")
			case <-time.After(5 * time.Millisecond):
			}
		}
		return dc.Send(b)
	})
	dc.OnMessage(func(m webrtc.DataChannelMessage) { s.Feed(m.Data) })
	dc.OnClose(func() { s.Close() })
	dc.OnOpen(func() { p.setStream(s, attempt) })
}
func sDone(dc *webrtc.DataChannel) <-chan struct{} {
	if dc.ReadyState() == webrtc.DataChannelStateClosed {
		c := make(chan struct{})
		close(c)
		return c
	}
	return nil
}
func gather(ctx context.Context, pc *webrtc.PeerConnection, desc webrtc.SessionDescription) error {
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(desc); err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func directServers(servers []webrtc.ICEServer) []webrtc.ICEServer {
	var direct []webrtc.ICEServer
	for _, server := range servers {
		var urls []string
		for _, u := range server.URLs {
			if strings.HasPrefix(u, "stun:") || strings.HasPrefix(u, "stuns:") {
				urls = append(urls, u)
			}
		}
		if len(urls) > 0 {
			server.URLs = urls
			direct = append(direct, server)
		}
	}
	return direct
}
func (p *Peer) AcceptOffer(ctx context.Context, offer protocol.Signal) error {
	ice := p.ICE
	if offer.Policy == "direct" {
		ice = directServers(ice)
	}
	pc, err := p.connection(ice, offer.Attempt)
	if err != nil {
		return err
	}
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() == "rpc" {
			p.attach(dc, offer.Attempt)
		} else {
			_ = dc.Close()
		}
	})
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.SDP}); err != nil {
		return err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return err
	}
	if err = gather(ctx, pc, answer); err != nil {
		return err
	}
	p.mu.Lock()
	current := p.pc == pc
	p.mu.Unlock()
	if !current {
		return context.Canceled
	}
	return p.Send(protocol.Signal{Type: "answer", SDP: pc.LocalDescription().SDP, Attempt: offer.Attempt})
}
func (p *Peer) tryRTC(ctx context.Context, ice []webrtc.ICEServer, policy string) (net.Conn, error) {
	attempt := identity.Random(12)
	p.Expect(attempt)
	pc, err := p.connection(ice, attempt)
	if err != nil {
		return nil, err
	}
	dc, err := pc.CreateDataChannel("rpc", nil)
	if err != nil {
		return nil, err
	}
	p.attach(dc, attempt)
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return nil, err
	}
	if err = gather(ctx, pc, offer); err != nil {
		return nil, err
	}
	if err = p.Send(protocol.Signal{Type: "offer", SDP: pc.LocalDescription().SDP, Attempt: attempt, Policy: policy}); err != nil {
		return nil, err
	}
	for {
		select {
		case answer := <-p.answers:
			if answer.Attempt != attempt {
				continue
			}
			if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
				return nil, err
			}
			goto answered
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
answered:
	for {
		select {
		case stream := <-p.ready:
			if stream.Attempt != attempt {
				stream.Conn.Close()
				continue
			}
			kind := "p2p"
			if sctp := pc.SCTP(); sctp != nil {
				if pair, err := sctp.Transport().ICETransport().GetSelectedCandidatePair(); err == nil && pair != nil {
					if pair.Local.Typ == webrtc.ICECandidateTypeRelay || pair.Remote.Typ == webrtc.ICECandidateTypeRelay {
						kind = "turn"
					}
				}
			}
			p.mu.Lock()
			p.Transport = kind
			p.mu.Unlock()
			return stream.Conn, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
func (p *Peer) Connect(ctx context.Context, policy string, iceTimeout time.Duration) (net.Conn, error) {
	if policy != "auto" && policy != "p2p" && policy != "relay" {
		return nil, fmt.Errorf("transport must be auto, p2p or relay")
	}
	if policy != "relay" {
		direct := directServers(p.ICE)
		attempt, cancel := context.WithTimeout(ctx, iceTimeout)
		stream, err := p.tryRTC(attempt, direct, "direct")
		cancel()
		if err == nil {
			return stream, nil
		}
		if policy == "p2p" {
			return nil, fmt.Errorf("P2P_FAILED: %w", err)
		}
		p.mu.Lock()
		p.Fallback = "P2P_FAILED"
		p.mu.Unlock()
		// A TURN attempt uses a fresh PeerConnection and only follows failed direct ICE.
		if len(p.ICE) > 0 {
			p.closeTransport()
			for {
				select {
				case <-p.answers:
					continue
				default:
					goto drained
				}
			}
		drained:
			attempt, cancel = context.WithTimeout(ctx, iceTimeout)
			stream, err = p.tryRTC(attempt, p.ICE, "all")
			cancel()
			if err == nil {
				return stream, nil
			}
			p.mu.Lock()
			p.Fallback = "ICE_FAILED"
			p.mu.Unlock()
		}
	}
	p.closeTransport()
	attempt := identity.Random(12)
	p.Expect(attempt)
	p.mu.Lock()
	p.Transport = "relay"
	p.attempt = attempt
	p.mu.Unlock()
	s := NewStream(func(b []byte) error { return p.Send(protocol.Signal{Type: "data", Data: b, Attempt: attempt}) })
	p.mu.Lock()
	p.stream = s
	p.mu.Unlock()
	if err := p.Send(protocol.Signal{Type: "relay", Attempt: attempt}); err != nil {
		return nil, err
	}
	return s, nil
}
func (p *Peer) Ready() <-chan Ready      { return p.ready }
func (p *Peer) Context() context.Context { return p.ctx }
func (p *Peer) Status() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	status := map[string]any{"transport": p.Transport, "fallbackReason": p.Fallback, "channelId": p.Channel}
	if pc := p.pc; pc != nil {
		if sctp := pc.SCTP(); sctp != nil {
			if pair, err := sctp.Transport().ICETransport().GetSelectedCandidatePair(); err == nil && pair != nil {
				status["candidateTypeLocal"] = pair.Local.Typ.String()
				status["candidateTypeRemote"] = pair.Remote.Typ.String()
				kind := "p2p"
				if pair.Local.Typ == webrtc.ICECandidateTypeRelay || pair.Remote.Typ == webrtc.ICECandidateTypeRelay {
					kind = "turn"
				}
				status["transport"] = kind
			}
		}
	}
	return status
}
