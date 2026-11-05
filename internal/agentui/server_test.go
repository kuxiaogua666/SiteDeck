package agentui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"sitedeck/internal/agent"
	"sitedeck/internal/identity"
	"sitedeck/internal/protocol"
)

func uiTestServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	c, err := NewController(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := Listen(context.Background(), c, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	body, _ := json.Marshal(map[string]string{"token": s.descriptor.Secret})
	req, _ := http.NewRequest("POST", s.URL+"/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var credentials struct {
		Token string
		CSRF  string `json:"csrfToken"`
	}
	if err = json.NewDecoder(response.Body).Decode(&credentials); err != nil || credentials.Token == "" || credentials.CSRF == "" {
		t.Fatal("login failed", err)
	}
	return s, credentials.Token, credentials.CSRF
}
func uiRequest(t *testing.T, s *Server, path, method, token, csrf, origin string, body any) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, s.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response.StatusCode, data
}
func TestUIAuthenticationAndBootstrap(t *testing.T) {
	s, token, csrf := uiTestServer(t)
	for _, item := range []struct {
		path, method, token, csrf, origin string
		want                              int
	}{
		{"/api/status", "GET", "", "", "", 401},
		{"/api/stop", "POST", token, "", "", 403},
		{"/api/stop", "POST", token, csrf, "http://localhost:9999", 403},
		{"/api/status", "GET", token, "", s.URL, 200},
		{"/control/open", "POST", token, csrf, "", 403},
	} {
		status, _ := uiRequest(t, s, item.path, item.method, item.token, item.csrf, item.origin, nil)
		if status != item.want {
			t.Fatalf("%s: got %d want %d", item.path, status, item.want)
		}
	}
	req, _ := http.NewRequest("GET", s.URL+"/api/status", nil)
	req.Host = "evil.example"
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("Host not checked")
	}
	path, err := s.NewLaunchFile()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatal("launch file permissions")
		}
	}
	match := regexp.MustCompile(`name="token" value="([a-f0-9]+)"`).FindSubmatch(b)
	if len(match) != 2 {
		t.Fatal("missing launch token")
	}
	form := url.Values{"token": {string(match[1])}}
	response, err = http.PostForm(s.URL+"/open", form)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !bytes.Contains(data, []byte("window.__AGENT_SESSION__=")) {
		t.Fatal("bootstrap failed")
	}
	if response.Header.Get("Set-Cookie") != "" {
		t.Fatal("localhost cookie must not be used")
	}
	if response.Header.Get("Location") != "" {
		t.Fatal("bootstrap must not place credentials in redirects")
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("launch file not deleted")
	}
	response, err = http.PostForm(s.URL+"/open", form)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("one-time launch token reusable")
	}
}
func TestUISettingsRedactionAndLifecycle(t *testing.T) {
	s, token, csrf := uiTestServer(t)
	ctx := context.Background()
	c := s.Controller
	settings := DefaultSettings()
	settings.RemoteEnabled = false
	settings.RegistrationToken = identity.Random(32)
	status, _ := uiRequest(t, s, "/api/config", "POST", token, csrf, s.URL, map[string]any{"settings": settings, "restart": true})
	if status != 200 {
		t.Fatal("save failed")
	}
	status, data := uiRequest(t, s, "/api/status", "GET", token, "", s.URL, nil)
	if status != 200 || bytes.Contains(data, []byte(settings.RegistrationToken)) {
		t.Fatal("registration credential leaked")
	}
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	request := protocol.Request{Method: "host.exec", Params: json.RawMessage(`{"command":"echo test"}`)}
	if _, rpcErr := c.Call(ctx, "local/test", request); rpcErr == nil || rpcErr.Code != "PERMISSION_DENIED" {
		t.Fatal("disabled permission bypassed")
	}
	// The CLI and UI must not run a second copy of the same identity.
	lockCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := agent.RunStored(lockCtx, agent.RuntimeOptions{StateFile: c.StatePath(), LocalOnly: true}); err == nil {
		t.Fatal("duplicate identity execution accepted")
	}
	before, _ := identity.Load(c.StatePath())
	settings.Permissions = []string{"host"}
	settings.RegistrationToken = ""
	if err := c.Save(ctx, settings, true); err != nil {
		t.Fatal(err)
	}
	saved, err := LoadSettings(c.ConfigPath())
	if err != nil || saved.RegistrationToken == "" {
		t.Fatal("blank credential must preserve previous value")
	}
	if err = c.Renew(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := identity.Load(c.StatePath())
	if before.GuestToken == after.GuestToken {
		t.Fatal("renew did not rotate guest token")
	}
	if err = c.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := c.Call(ctx, "local/test", request); rpcErr == nil {
		t.Fatal("stopped service accepted command")
	}
	if info := c.Info(); info["running"] != false || info["ready"] != false {
		t.Fatal("service retained resources")
	}
	// Local tools remain usable after remote guest access expires.
	after.GuestExpiresAt = time.Now().Add(-time.Hour)
	if err = after.Save(c.StatePath()); err != nil {
		t.Fatal(err)
	}
	if err = c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	_, data = uiRequest(t, s, "/api/status", "GET", token, "", s.URL, nil)
	for _, secret := range []string{after.GuestToken, saved.RegistrationToken} {
		if strings.Contains(string(data), secret) {
			t.Fatal("status leaked secret")
		}
	}
}
