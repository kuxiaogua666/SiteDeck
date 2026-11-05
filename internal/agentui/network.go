package agentui

// Network debugging belongs to the authenticated local UI only. It is not
// registered as a remote Agent RPC and is not linked into headless builds.
import (
	"context"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"
	"time"

	"sitedeck/internal/identity"
)

const networkBufferLimit = 1 << 20

type networkOpen struct {
	Mode   string `json:"mode"`
	Local  string `json:"local"`
	Remote string `json:"remote"`
}
type networkEvent struct {
	Sequence  uint64    `json:"sequence"`
	Time      time.Time `json:"time"`
	Direction string    `json:"direction"`
	Peer      string    `json:"peer,omitempty"`
	Data      []byte    `json:"data,omitempty"`
	Message   string    `json:"message,omitempty"`
}
type networkPeer struct {
	conn    net.Conn
	writeMu sync.Mutex
}
type networkSession struct {
	ID       string
	owner    string
	mode     string
	local    string
	remote   string
	mu       sync.Mutex
	writeMu  sync.Mutex
	listener net.Listener
	udp      *net.UDPConn
	peers    map[string]*networkPeer
	closed   bool
	events   []networkEvent
	sequence uint64
	buffered int
	rx       uint64
	tx       uint64
}
type networkManager struct {
	mu       sync.Mutex
	sessions map[string]*networkSession
	closed   bool
}

func newNetworkManager() *networkManager {
	return &networkManager{sessions: map[string]*networkSession{}}
}
func (m *networkManager) open(ctx context.Context, owner string, p networkOpen) (*networkSession, error) {
	s := &networkSession{ID: identity.Random(16), owner: owner, mode: p.Mode, peers: map[string]*networkPeer{}}
	switch p.Mode {
	case "tcp-client":
		if p.Remote == "" {
			return nil, fmt.Errorf("请填写目标地址和端口")
		}
		d := net.Dialer{Timeout: 8 * time.Second}
		if p.Local != "" {
			a, err := net.ResolveTCPAddr("tcp", p.Local)
			if err != nil {
				return nil, err
			}
			d.LocalAddr = a
		}
		conn, err := d.DialContext(ctx, "tcp", p.Remote)
		if err != nil {
			return nil, err
		}
		s.local = conn.LocalAddr().String()
		s.remote = conn.RemoteAddr().String()
		s.peers["server"] = &networkPeer{conn: conn}
	case "tcp-server":
		if p.Local == "" {
			p.Local = "127.0.0.1:48771"
		}
		var lc net.ListenConfig
		listener, err := lc.Listen(ctx, "tcp", p.Local)
		if err != nil {
			return nil, err
		}
		s.listener = listener
		s.local = listener.Addr().String()
	case "udp":
		if p.Local == "" {
			p.Local = "127.0.0.1:48772"
		}
		addr, err := net.ResolveUDPAddr("udp", p.Local)
		if err != nil {
			return nil, err
		}
		conn, err := net.ListenUDP("udp", addr)
		if err != nil {
			return nil, err
		}
		s.udp = conn
		s.local = conn.LocalAddr().String()
		s.remote = p.Remote
	default:
		return nil, fmt.Errorf("请选择 UDP、TCP Client 或 TCP Server")
	}
	m.mu.Lock()
	count := 0
	for _, item := range m.sessions {
		if item.owner == owner {
			count++
		}
	}
	if m.closed || count >= 12 || len(m.sessions) >= 32 {
		m.mu.Unlock()
		s.close()
		return nil, fmt.Errorf("网络调试会话已达上限或界面正在退出")
	}
	m.sessions[s.ID] = s
	m.mu.Unlock()
	s.log("info", "", nil, "已打开 "+p.Mode+" · "+s.local)
	switch p.Mode {
	case "tcp-client":
		go s.readTCP("server", s.peers["server"])
	case "tcp-server":
		go s.accept()
	case "udp":
		go s.readUDP()
	}
	return s, nil
}
func (m *networkManager) get(owner, id string) (*networkSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil || s.owner != owner {
		return nil, fmt.Errorf("网络调试会话不存在，请重新打开")
	}
	return s, nil
}
func (m *networkManager) remove(owner, id string) error {
	m.mu.Lock()
	s := m.sessions[id]
	if s == nil || s.owner != owner {
		m.mu.Unlock()
		return fmt.Errorf("网络调试会话不存在")
	}
	delete(m.sessions, id)
	m.mu.Unlock()
	s.close()
	return nil
}
func (m *networkManager) closeOwner(owner string) {
	m.mu.Lock()
	var sessions []*networkSession
	for id, s := range m.sessions {
		if s.owner == owner {
			sessions = append(sessions, s)
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
	for _, s := range sessions {
		s.close()
	}
}
func (m *networkManager) close() {
	m.mu.Lock()
	m.closed = true
	sessions := m.sessions
	m.sessions = map[string]*networkSession{}
	m.mu.Unlock()
	for _, s := range sessions {
		s.close()
	}
}
func (s *networkSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	var conns []net.Conn
	for _, p := range s.peers {
		conns = append(conns, p.conn)
	}
	listener, udp := s.listener, s.udp
	s.mu.Unlock()
	if listener != nil {
		listener.Close()
	}
	if udp != nil {
		udp.Close()
	}
	for _, c := range conns {
		c.Close()
	}
	s.log("info", "", nil, "会话已关闭")
}
func (s *networkSession) log(direction, peer string, data []byte, message string) {
	// Preserve UDP packet boundaries, including empty datagrams.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	s.events = append(s.events, networkEvent{Sequence: s.sequence, Time: time.Now().UTC(), Direction: direction, Peer: peer, Data: append([]byte(nil), data...), Message: message})
	s.buffered += len(data) + len(message)
	if direction == "rx" {
		s.rx += uint64(len(data))
	}
	if direction == "tx" {
		s.tx += uint64(len(data))
	}
	for len(s.events) > 1024 || s.buffered > networkBufferLimit {
		s.buffered -= len(s.events[0].Data) + len(s.events[0].Message)
		s.events[0] = networkEvent{}
		s.events = s.events[1:]
	}
}
func (s *networkSession) snapshot(cursor uint64) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := make([]networkEvent, 0)
	next := cursor
	size := 0
	for _, e := range s.events {
		if e.Sequence <= cursor {
			continue
		}
		if len(events) >= 128 || (size > 0 && size+len(e.Data)+len(e.Message) > 48<<10) {
			break
		}
		events = append(events, e)
		next = e.Sequence
		size += len(e.Data) + len(e.Message)
	}
	peers := make([]map[string]string, 0, len(s.peers))
	for id, p := range s.peers {
		peers = append(peers, map[string]string{"id": id, "address": p.conn.RemoteAddr().String()})
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i]["id"] < peers[j]["id"] })
	overflow := len(s.events) > 0 && cursor+1 < s.events[0].Sequence
	return map[string]any{"id": s.ID, "mode": s.mode, "local": s.local, "remote": s.remote, "closed": s.closed, "peers": peers, "rxBytes": s.rx, "txBytes": s.tx, "events": events, "nextCursor": next, "overflow": overflow}
}
func (s *networkSession) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if !closed {
				s.log("error", "", nil, err.Error())
				s.close()
			}
			return
		}
		id := identity.Random(6)
		p := &networkPeer{conn: conn}
		s.mu.Lock()
		reject := s.closed || len(s.peers) >= 32
		if !reject {
			s.peers[id] = p
		}
		s.mu.Unlock()
		if reject {
			conn.Close()
			continue
		}
		s.log("info", id, nil, "客户端已连接 · "+conn.RemoteAddr().String())
		go s.readTCP(id, p)
	}
}
func (s *networkSession) readTCP(id string, p *networkPeer) {
	defer p.conn.Close()
	buffer := make([]byte, 8192)
	for {
		n, err := p.conn.Read(buffer)
		if n > 0 {
			s.log("rx", id, buffer[:n], "")
		}
		if err != nil {
			s.mu.Lock()
			delete(s.peers, id)
			closed := s.closed
			s.mu.Unlock()
			if !closed {
				if err != io.EOF {
					s.log("error", id, nil, err.Error())
				}
				s.log("info", id, nil, "连接已断开")
			}
			if s.mode == "tcp-client" {
				s.close()
			}
			return
		}
	}
}
func (s *networkSession) readUDP() {
	buffer := make([]byte, 65535)
	for {
		n, addr, err := s.udp.ReadFromUDP(buffer)
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if !closed {
				s.log("error", "", nil, err.Error())
				s.close()
			}
			return
		}
		s.log("rx", addr.String(), buffer[:n], "")
	}
}
func (s *networkSession) send(ctx context.Context, peer, remote string, data []byte) error {
	if len(data) > 32<<10 {
		return fmt.Errorf("单次发送最多 32 KiB")
	}
	s.mu.Lock()
	closed := s.closed
	selected := map[string]*networkPeer{}
	for id, p := range s.peers {
		if peer == "" || peer == id {
			selected[id] = p
		}
	}
	s.mu.Unlock()
	if closed {
		return fmt.Errorf("会话已关闭")
	}
	if s.mode == "udp" {
		if remote == "" {
			remote = s.remote
		}
		if remote == "" {
			return fmt.Errorf("请填写 UDP 目标地址和端口")
		}
		// Resolve with a context so an invalid DNS name cannot stall shutdown.
		host, port, err := net.SplitHostPort(remote)
		if err != nil {
			return err
		}
		if net.ParseIP(host) == nil {
			addrs, err := net.DefaultResolver.LookupHost(ctx, host)
			if err != nil {
				return err
			}
			if len(addrs) == 0 {
				return fmt.Errorf("目标地址解析失败")
			}
			host = addrs[0]
		}
		addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, port))
		if err != nil {
			return err
		}
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		_ = s.udp.SetWriteDeadline(time.Now().Add(2 * time.Second))
		n, err := s.udp.WriteToUDP(data, addr)
		if err != nil {
			return err
		}
		s.log("tx", addr.String(), data[:n], "")
		return nil
	}
	if len(selected) == 0 {
		return fmt.Errorf("尚无可发送的 TCP 连接")
	}
	var first error
	for id, p := range selected {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.writeMu.Lock()
		deadline := time.Now().Add(2 * time.Second)
		if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
			deadline = end
		}
		_ = p.conn.SetWriteDeadline(deadline)
		remaining := data
		for len(remaining) > 0 {
			n, err := p.conn.Write(remaining)
			if n > 0 {
				s.log("tx", id, remaining[:n], "")
				remaining = remaining[n:]
			}
			if err != nil {
				if first == nil {
					first = err
				}
				break
			}
			if n == 0 {
				if first == nil {
					first = io.ErrShortWrite
				}
				break
			}
		}
		p.writeMu.Unlock()
	}
	return first
}
func (s *networkSession) disconnect(peer string) error {
	s.mu.Lock()
	p := s.peers[peer]
	s.mu.Unlock()
	if p == nil {
		return fmt.Errorf("客户端已断开")
	}
	return p.conn.Close()
}
