//go:build !windows

package agentui

import (
	"context"
	"encoding/json"
	"github.com/creack/pty"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sitedeck/internal/client"
	"sitedeck/internal/hub"
	"sitedeck/internal/identity"
	"sitedeck/internal/protocol"
	"sitedeck/internal/session"
)

func uiCall(t *testing.T, c *Controller, method string, params any) any {
	t.Helper()
	b, _ := json.Marshal(params)
	result, err := c.Call(context.Background(), "terminal-test", protocol.Request{Method: method, Params: b, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestUILocalTerminal(t *testing.T) {
	c, err := NewController(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	settings := DefaultSettings()
	settings.RemoteEnabled = false
	settings.Permissions = []string{"host"}
	ctx := context.Background()
	if err = c.Save(ctx, settings, false); err != nil {
		t.Fatal(err)
	}
	if err = c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	opened := uiCall(t, c, "host.shell", map[string]any{"shell": "/bin/sh", "rows": 24, "cols": 80}).(map[string]any)
	id := opened["id"].(string)
	uiCall(t, c, "session.attach", map[string]any{"id": id})
	uiCall(t, c, "session.resize", map[string]any{"id": id, "rows": 30, "cols": 120})
	uiCall(t, c, "session.write", map[string]any{"id": id, "text": "printf 'UI_TERMINAL_%s\\n' OK\n"})
	cursor := ""
	output := ""
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := uiCall(t, c, "session.read", map[string]any{"id": id, "cursor": cursor, "waitMs": 200}).(*session.ReadResult)
		cursor = r.Next
		output += string(r.Data)
		if strings.Contains(output, "UI_TERMINAL_OK") {
			break
		}
	}
	if !strings.Contains(output, "UI_TERMINAL_OK") {
		t.Fatalf("shell output missing: %q", output)
	}
	uiCall(t, c, "session.detach", map[string]any{"id": id})
}
func TestUIAgentAcceptsExistingCLIProtocol(t *testing.T) {
	c, err := NewController(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	key := identity.Random(32)
	h := hub.New(key)
	defer h.Close()
	server := httptest.NewServer(h.Handler())
	defer server.Close()
	settings := DefaultSettings()
	settings.Hub = "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	settings.RegistrationToken = key
	settings.Permissions = []string{"host"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = c.Save(ctx, settings, false); err != nil {
		t.Fatal(err)
	}
	if err = c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ := identity.Load(c.StatePath())
	cfg := client.Config{Hub: settings.Hub, Device: state.Device, Fingerprint: state.Fingerprint(), Token: state.GuestToken, Transport: "relay"}
	var conn *client.Connection
	for ctx.Err() == nil {
		conn, err = client.Open(ctx, cfg)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	result, rpcErr := conn.RPC.Call(ctx, "host.exec", map[string]any{"command": "printf UI_REMOTE_OK"})
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	data, _ := json.Marshal(result)
	if !strings.Contains(string(data), "UI_REMOTE_OK") {
		t.Fatal("remote CLI command did not reach UI Agent")
	}
}

func TestUISerialAndReadOnlyMembership(t *testing.T) {
	c, err := NewController(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	settings := DefaultSettings()
	settings.RemoteEnabled = false
	settings.Permissions = []string{"serial"}
	if err = c.Save(context.Background(), settings, false); err != nil {
		t.Fatal(err)
	}
	if err = c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	opened := uiCall(t, c, "serial.connect", map[string]any{"port": slave.Name(), "baudRate": 115200}).(map[string]any)
	id := opened["id"].(string)
	uiCall(t, c, "session.attach", map[string]any{"id": id, "readOnly": true})
	b, _ := json.Marshal(map[string]any{"id": id, "text": "must-not-write"})
	if _, rpcErr := c.Call(context.Background(), "terminal-test", protocol.Request{Method: "session.write", Params: b}); rpcErr == nil || rpcErr.Code != "PERMISSION_DENIED" {
		t.Fatal("read-only serial member could write")
	}
	if _, err = master.Write([]byte("UI_SERIAL_RX")); err != nil {
		t.Fatal(err)
	}
	output := ""
	cursor := ""
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r := uiCall(t, c, "session.read", map[string]any{"id": id, "cursor": cursor, "waitMs": 200, "source": "rx"}).(*session.ReadResult)
		cursor = r.Next
		output += string(r.Data)
		if strings.Contains(output, "UI_SERIAL_RX") {
			break
		}
	}
	if !strings.Contains(output, "UI_SERIAL_RX") {
		t.Fatal("serial RX missing")
	}
	uiCall(t, c, "session.close", map[string]any{"id": id})
}
