package agentui

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func waitNetwork(t *testing.T, s *networkSession, predicate func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		value := s.snapshot(0)
		if predicate(value) {
			return value
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("network event did not arrive")
	return nil
}
func TestNetworkUDPBinaryAndOwnerIsolation(t *testing.T) {
	m := newNetworkManager()
	defer m.close()
	s, err := m.open(context.Background(), "one", networkOpen{Mode: "udp", Local: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	data := []byte{0, 255, 0x1b, 13, 10}
	if err = s.send(context.Background(), "", peer.LocalAddr().String(), data); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	b := make([]byte, 64)
	n, addr, err := peer.ReadFrom(b)
	if err != nil || !bytes.Equal(b[:n], data) {
		t.Fatalf("binary UDP send: %v", err)
	}
	if _, err = peer.WriteTo(data, addr); err != nil {
		t.Fatal(err)
	}
	value := waitNetwork(t, s, func(v map[string]any) bool { return v["rxBytes"].(uint64) == uint64(len(data)) })
	events := value["events"].([]networkEvent)
	if !bytes.Equal(events[len(events)-1].Data, data) || events[len(events)-1].Time.IsZero() {
		t.Fatal("UDP RX lost bytes or timestamp")
	}
	if _, err = m.get("other", s.ID); err == nil {
		t.Fatal("cross-owner read accepted")
	}
	if err = m.remove("other", s.ID); err == nil {
		t.Fatal("cross-owner close accepted")
	}
	m.closeOwner("one")
	if _, err = m.get("one", s.ID); err == nil {
		t.Fatal("expired owner retained session")
	}
	reuse, err := net.ListenPacket("udp", s.local)
	if err != nil {
		t.Fatal("UDP socket retained", err)
	}
	reuse.Close()
}
func TestNetworkTCPClientServerAndDisconnect(t *testing.T) {
	m := newNetworkManager()
	defer m.close()
	server, err := m.open(context.Background(), "test", networkOpen{Mode: "tcp-server", Local: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	client, err := m.open(context.Background(), "test", networkOpen{Mode: "tcp-client", Remote: server.local})
	if err != nil {
		t.Fatal(err)
	}
	extra, err := net.DialTimeout("tcp", server.local, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	value := waitNetwork(t, server, func(v map[string]any) bool { return len(v["peers"].([]map[string]string)) == 2 })
	if err = client.send(context.Background(), "server", "", []byte{0, 0xff, 1}); err != nil {
		t.Fatal(err)
	}
	waitNetwork(t, server, func(v map[string]any) bool { return v["rxBytes"].(uint64) == 3 })
	if err = server.send(context.Background(), "", "", []byte("BROADCAST")); err != nil {
		t.Fatal(err)
	}
	waitNetwork(t, client, func(v map[string]any) bool { return v["rxBytes"].(uint64) == 9 })
	_ = extra.SetReadDeadline(time.Now().Add(time.Second))
	data := make([]byte, 9)
	if _, err = io.ReadFull(extra, data); err != nil || string(data) != "BROADCAST" {
		t.Fatalf("TCP broadcast: %v", err)
	}
	// Select only the second socket, proving targeted sending is not broadcast.
	var target string
	for _, p := range value["peers"].([]map[string]string) {
		if p["address"] == extra.LocalAddr().String() {
			target = p["id"]
		}
	}
	if target == "" {
		t.Fatal("second TCP client missing")
	}
	if err = server.send(context.Background(), target, "", []byte("TARGET")); err != nil {
		t.Fatal(err)
	}
	data = make([]byte, 6)
	if _, err = io.ReadFull(extra, data); err != nil || string(data) != "TARGET" {
		t.Fatal("targeted TCP send failed", err)
	}
	if err = server.disconnect(target); err != nil {
		t.Fatal(err)
	}
	waitNetwork(t, server, func(v map[string]any) bool { return len(v["peers"].([]map[string]string)) == 1 })
	if err = server.send(context.Background(), "missing", "", data); err == nil {
		t.Fatal("stale peer silently accepted")
	}
	m.close()
	waitNetwork(t, client, func(v map[string]any) bool { return v["closed"].(bool) })
	reuse, err := net.Listen("tcp", server.local)
	if err != nil {
		t.Fatal("TCP listener retained", err)
	}
	reuse.Close()
}
func TestNetworkBufferIsBounded(t *testing.T) {
	s := &networkSession{peers: map[string]*networkPeer{}}
	for i := 0; i < 300; i++ {
		s.log("rx", "peer", make([]byte, 8192), "")
	}
	value := s.snapshot(0)
	if !value["overflow"].(bool) || s.buffered > networkBufferLimit {
		t.Fatal("network log is not bounded")
	}
	events := value["events"].([]networkEvent)
	size := 0
	for _, e := range events {
		size += len(e.Data)
	}
	if size > 48<<10 {
		t.Fatal("poll response exceeds byte page size")
	}
	if _, err := newNetworkManager().open(context.Background(), "test", networkOpen{Mode: "invalid"}); err == nil {
		t.Fatal("invalid mode accepted")
	}
}
func TestNetworkHTTPRequiresSessionAndCSRF(t *testing.T) {
	s, token, csrf := uiTestServer(t)
	body := map[string]any{"mode": "udp", "local": "127.0.0.1:0"}
	status, _ := uiRequest(t, s, "/api/network/open", "POST", "", "", s.URL, body)
	if status != 401 {
		t.Fatal("network API unauthenticated")
	}
	status, _ = uiRequest(t, s, "/api/network/open", "POST", token, "", s.URL, body)
	if status != 403 {
		t.Fatal("network API missing CSRF check")
	}
	status, _ = uiRequest(t, s, "/api/network/open", "POST", token, csrf, s.URL, body)
	if status != 200 {
		t.Fatal("authorized local network tool failed")
	}
}
