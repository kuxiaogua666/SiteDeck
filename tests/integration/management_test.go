//go:build !windows

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"sitedeck/internal/agent"
	"sitedeck/internal/client"
	"sitedeck/internal/hub"
	"sitedeck/internal/identity"
	"sitedeck/internal/protocol"
)

type management struct {
	site         *site
	hub          *hub.Hub
	server       *httptest.Server
	http         *http.Client
	csrf         string
	admin        string
	registration string
	statePath    string
	stopAgent    func()
}

func managed(t *testing.T, allow string) *management {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	state, err := identity.Init(dir + "/agent.json")
	if err != nil {
		t.Fatal(err)
	}
	state.Name = "初始现场设备"
	registration, admin := identity.Random(32), identity.Random(32)
	statePath := dir + "/hub.json"
	h, err := hub.Open(registration, admin, statePath)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h.Handler())
	jar, _ := cookiejar.New(nil)
	m := &management{hub: h, server: server, http: &http.Client{Jar: jar}, admin: admin, registration: registration, statePath: statePath}
	m.site = &site{ctx: ctx, state: state, cfg: client.Config{Hub: "ws" + strings.TrimPrefix(server.URL, "http") + "/ws", Device: state.Device, Fingerprint: state.Fingerprint(), Token: state.GuestToken, Transport: "relay", ICETimeout: 2 * time.Second}}
	done := make(chan struct{})
	agentCtx, stop := context.WithCancel(ctx)
	go func() {
		defer close(done)
		_ = agent.Run(agentCtx, agent.Config{Hub: m.site.cfg.Hub, State: state, Registration: registration, Allow: allow})
	}()
	m.stopAgent = func() {
		stop()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("management Agent did not stop")
		}
	}
	t.Cleanup(func() { cancel(); m.stopAgent(); h.Close(); server.Close() })
	m.login(t, admin)
	waitFor(t, func() bool { d := m.overview(t); return len(d.Devices) == 1 && d.Devices[0].Online })
	return m
}
func waitFor(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("management state did not converge")
}
func (m *management) request(t *testing.T, method, path string, body any, status int) json.RawMessage {
	t.Helper()
	var data io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		data = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, m.server.URL+"/admin/api/"+path, data)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if m.csrf != "" {
		req.Header.Set("X-CSRF-Token", m.csrf)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("management %s %s: status %d, wanted %d; %s", method, path, resp.StatusCode, status, string(b))
	}
	return b
}
func (m *management) login(t *testing.T, key string) {
	t.Helper()
	raw := m.request(t, "POST", "login", map[string]string{"secret": key}, 200)
	var response struct {
		CSRF string `json:"csrfToken"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	m.csrf = response.CSRF
}

type overview struct {
	Devices     []hub.DeviceView  `json:"devices"`
	Grants      []hub.Grant       `json:"grants"`
	Connections []hub.ChannelView `json:"connections"`
}

func (m *management) overview(t *testing.T) overview {
	t.Helper()
	var result overview
	if err := json.Unmarshal(m.request(t, "GET", "overview", nil, 200), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func (m *management) grant(t *testing.T, scopes []string, expiry time.Time) (string, string) {
	t.Helper()
	raw := m.request(t, "POST", "grants", map[string]any{"deviceId": m.site.state.Device, "name": "现场临时授权", "scopes": scopes, "expiresAt": expiry}, 201)
	var result struct {
		Grant hub.Grant `json:"grant"`
		Token string    `json:"accessToken"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Token == "" {
		t.Fatal("missing one-time access token")
	}
	return result.Grant.ID, result.Token
}
func expectConnectCode(t *testing.T, s *site, cfg client.Config, code string) {
	t.Helper()
	c, err := client.Open(s.ctx, cfg)
	if c != nil {
		c.Close()
	}
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestManagementAuthenticationAndCSRF(t *testing.T) {
	m := managed(t, "host")
	response, err := http.Get(m.server.URL + "/admin/api/overview")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("unprotected management API")
	}
	savedCSRF := m.csrf
	m.csrf = "wrong"
	m.request(t, "PATCH", "devices/"+m.site.state.Device, map[string]string{"name": "must not change"}, 403)
	m.csrf = savedCSRF
	m.request(t, "POST", "login", map[string]string{"secret": m.registration}, 401)
	req, _ := http.NewRequest("PATCH", m.server.URL+"/admin/api/devices/"+m.site.state.Device, strings.NewReader(`{"name":"cross origin"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://untrusted.invalid")
	req.Header.Set("X-CSRF-Token", m.csrf)
	resp, err := m.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("cross-origin update accepted")
	}
	static, err := m.http.Get(m.server.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	static.Body.Close()
	if static.StatusCode != 200 || static.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("management UI or CSP missing")
	}
	m.request(t, "POST", "logout", map[string]any{}, 200)
	m.request(t, "GET", "overview", nil, 401)
}
func TestManagementDeviceRenamePersists(t *testing.T) {
	m := managed(t, "host,serial")
	id := m.site.state.Device
	m.request(t, "PATCH", "devices/"+id, map[string]any{"name": "研发台架 · RK3568", "allowPaired": false}, 200)
	_, savedToken := m.grant(t, []string{"host"}, time.Now().Add(time.Hour))
	if d := m.overview(t).Devices[0]; d.Name != "研发台架 · RK3568" || d.AllowPaired || d.OS == "" || len(d.Capabilities) != 2 {
		t.Fatal("device metadata or rename missing")
	}
	m.stopAgent()
	waitFor(t, func() bool { return !m.overview(t).Devices[0].Online })
	restored, err := hub.Open(m.registration, m.admin, m.statePath)
	if err != nil {
		t.Fatal(err)
	}
	restoredServer := httptest.NewServer(restored.Handler())
	defer restoredServer.Close()
	defer restored.Close()
	m.server = restoredServer
	m.csrf = ""
	jar, _ := cookiejar.New(nil)
	m.http = &http.Client{Jar: jar}
	m.login(t, m.admin)
	d := m.overview(t).Devices[0]
	if d.Name != "研发台架 · RK3568" || d.AllowPaired || d.Online {
		t.Fatal("offline device name or policy not persisted")
	}
	newCtx, stop := context.WithCancel(m.site.ctx)
	done := make(chan struct{})
	newHub := "ws" + strings.TrimPrefix(restoredServer.URL, "http") + "/ws"
	go func() {
		defer close(done)
		_ = agent.Run(newCtx, agent.Config{Hub: newHub, State: m.site.state, Registration: m.registration, Allow: "host,serial"})
	}()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("restarted Agent did not stop")
		}
	}()
	waitFor(t, func() bool { return m.overview(t).Devices[0].Online })
	if m.overview(t).Devices[0].Name != "研发台架 · RK3568" {
		t.Fatal("Agent re-registration overwrote the display name")
	}
	cfg := m.site.cfg
	cfg.Hub = newHub
	cfg.AccessToken = savedToken
	c, err := client.Open(m.site.ctx, cfg)
	if err != nil {
		t.Fatal("persisted grant could not reconnect after Hub restart", err)
	}
	defer c.Close()
	call(t, c, "doctor", map[string]any{})
}
func TestManagementScopedGrantAndRevocation(t *testing.T) {
	m := managed(t, "host,ssh,serial")
	paired := m.site.open(t)
	large := object(t, call(t, paired, "host.exec", map[string]any{"command": "head -c 32768 /dev/zero"}))
	artifactID := large["artifact"].(map[string]any)["id"].(string)
	id, token := m.grant(t, []string{"serial"}, time.Now().Add(time.Hour))
	select {
	case <-paired.RPC.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("paired connection was not revoked")
	}
	expectConnectCode(t, m.site, m.site.cfg, "ACCESS_REQUIRED")
	cfg := m.site.cfg
	cfg.AccessToken = token
	c, err := client.Open(m.site.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	permissions := object(t, call(t, c, "permissions.get", map[string]any{}))
	if permissions["host"] != false || permissions["serial"] != true {
		t.Fatal(permissions)
	}
	ctx, cancel := context.WithTimeout(m.site.ctx, 3*time.Second)
	defer cancel()
	if _, err := c.RPC.Call(ctx, "host.exec", map[string]string{"command": "true"}); err == nil || err.Code != "PERMISSION_DENIED" {
		t.Fatal("grant bypassed host permission")
	}
	if _, err := c.RPC.Call(ctx, "artifact.read", map[string]string{"id": artifactID}); err == nil || err.Code != "PERMISSION_DENIED" {
		t.Fatal("grant leaked a host artifact")
	}
	call(t, c, "serial.list", map[string]any{})
	raw := m.request(t, "GET", "overview", nil, 200)
	if bytes.Contains(raw, []byte(token)) || bytes.Contains(raw, []byte("tokenHash")) || bytes.Contains(raw, []byte("signingKey")) {
		t.Fatal("management overview leaks credentials")
	}
	persisted, err := os.ReadFile(m.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte(token)) {
		t.Fatal("plaintext grant persisted")
	}
	m.request(t, "DELETE", "grants/"+id, nil, 200)
	select {
	case <-c.RPC.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("revoked grant retained its active connection")
	}
	expectConnectCode(t, m.site, cfg, "ACCESS_DENIED")
}
func TestManagementP2PDisconnectAndGrantExpiry(t *testing.T) {
	m := managed(t, "host")
	id, token := m.grant(t, []string{"host"}, time.Now().Add(time.Hour))
	cfg := m.site.cfg
	cfg.AccessToken = token
	cfg.Transport = "p2p"
	cfg.ICETimeout = 5 * time.Second
	c, err := client.Open(m.site.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	call(t, c, "doctor", map[string]any{})
	waitFor(t, func() bool {
		connections := m.overview(t).Connections
		return len(connections) == 1 && connections[0].Grant == id && connections[0].Transport == "p2p" && connections[0].AuthenticatedAt != nil
	})
	m.request(t, "DELETE", "connections/"+c.Peer.Channel, nil, 200)
	select {
	case <-c.RPC.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("admin disconnect did not close a P2P channel")
	}
	_, expiring := m.grant(t, []string{"host"}, time.Now().Add(2500*time.Millisecond))
	cfg.AccessToken = expiring
	cfg.Transport = "relay"
	short, err := client.Open(m.site.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer short.Close()
	call(t, short, "doctor", map[string]any{})
	select {
	case <-short.RPC.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("expired grant retained an active connection")
	}
	expectConnectCode(t, m.site, cfg, "ACCESS_EXPIRED")
	m.request(t, "PATCH", "devices/"+m.site.state.Device, map[string]bool{"blocked": true}, 200)
	cfg.AccessToken = token
	expectConnectCode(t, m.site, cfg, "DEVICE_BLOCKED")
}
func TestManagementGrantCannotEnableLocalCapability(t *testing.T) {
	m := managed(t, "serial")
	_, token := m.grant(t, []string{"host", "serial"}, time.Now().Add(time.Hour))
	cfg := m.site.cfg
	cfg.AccessToken = token
	c, err := client.Open(m.site.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	permissions := object(t, call(t, c, "permissions.get", map[string]any{}))
	if permissions["host"] != false || permissions["serial"] != true {
		t.Fatal("grant enabled a locally disabled capability")
	}
}
