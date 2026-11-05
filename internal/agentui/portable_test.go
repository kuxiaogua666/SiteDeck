package agentui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sitedeck/internal/agentconfig"
	"sitedeck/internal/identity"
	"sitedeck/internal/pairing"
	"strings"
	"testing"
)

func TestPortableConfigDoesNotCloneIdentity(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "site-agent.json")
	if err := os.WriteFile(config, []byte(`{"hub":"wss://example.test/ws","registrationToken":"`+strings.Repeat("a", 64)+`","permissions":["host"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := NewControllerWithConfig(filepath.Join(root, "one"), config)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewControllerWithConfig(filepath.Join(root, "two"), config)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	first, _ := identity.Load(a.StatePath())
	second, _ := identity.Load(b.StatePath())
	if first.Device == second.Device || first.GuestToken == second.GuestToken || first.Fingerprint() == second.Fingerprint() {
		t.Fatal("shared deployment settings cloned device identity")
	}
	code, err := a.PairText()
	if err != nil {
		t.Fatal(err)
	}
	p, err := pairing.Decode(code)
	if err != nil || p.Device != first.Device {
		t.Fatal("UI pairing text invalid", err)
	}
	settings, _ := agentconfig.LoadSettings(config)
	settings.Name = "renamed"
	if err = a.Save(context.Background(), settings, false); err != nil {
		t.Fatal(err)
	}
	saved, _ := agentconfig.LoadSettings(config)
	if saved.Name != "renamed" || a.ConfigPath() != config {
		t.Fatal("UI did not save back to selected config")
	}
}

func TestPairTextEndpointIsPrivateAndStatusIsRedacted(t *testing.T) {
	s, token, csrf := uiTestServer(t)
	status, _ := uiRequest(t, s, "/api/pair-text", "GET", "", "", "", nil)
	if status != 401 {
		t.Fatal("pairing text available without login")
	}
	settings := DefaultSettings()
	settings.Hub = "ws://127.0.0.1:48763/ws"
	settings.RemoteEnabled = false
	if err := s.Controller.Save(context.Background(), settings, false); err != nil {
		t.Fatal(err)
	}
	status, data := uiRequest(t, s, "/api/pair-text", "GET", token, csrf, s.URL, nil)
	if status != 200 {
		t.Fatal("pairing text unavailable")
	}
	var result struct {
		PairingText string `json:"pairingText"`
	}
	if json.Unmarshal(data, &result) != nil {
		t.Fatal("invalid response")
	}
	profile, err := pairing.Decode(result.PairingText)
	if err != nil {
		t.Fatal(err)
	}
	_, data = uiRequest(t, s, "/api/status", "GET", token, "", s.URL, nil)
	if strings.Contains(string(data), profile.Token) || strings.Contains(string(data), result.PairingText) {
		t.Fatal("pairing secret leaked through status")
	}
}
