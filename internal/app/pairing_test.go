package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sitedeck/internal/agentconfig"
	"sitedeck/internal/pairing"
	"strings"
	"testing"
	"time"
)

func TestSetupPersistsHubCredentialWithoutDeviceIdentity(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "hub.key")
	os.WriteFile(key, []byte(strings.Repeat("a", 64)), 0600)
	config := filepath.Join(root, "site-agent.json")
	_, err := configureAgent([]string{"--config", config, "--data-dir", filepath.Join(root, "data"), "--hub", "wss://example.test/ws", "--registration-file", key, "--allow", "host"})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := agentconfig.LoadSettings(config)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Hub != "wss://example.test/ws" || settings.RegistrationToken != strings.Repeat("a", 64) || len(settings.Permissions) != 1 {
		t.Fatal("configuration not persisted")
	}
	var raw map[string]any
	b, _ := os.ReadFile(config)
	json.Unmarshal(b, &raw)
	for _, k := range []string{"deviceId", "guestToken", "privateKey"} {
		if raw[k] != nil {
			t.Fatal("portable configuration copied device identity")
		}
	}
}
func TestPairImportProtectsOtherDeviceAndAllowsRotation(t *testing.T) {
	p := pairing.Profile{Hub: "wss://example.test/ws", Device: "dev-first", Fingerprint: strings.Repeat("a", 64), Token: strings.Repeat("b", 64), Expires: time.Now().Add(time.Hour)}
	code, err := pairing.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pair.json")
	if err = runPairImport(path, []string{"--text", code}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var local map[string]any
	if err = json.Unmarshal(b, &local); err != nil {
		t.Fatal(err)
	}
	local["proxy"] = "direct"
	b, err = json.Marshal(local)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	p.Token = strings.Repeat("c", 64)
	code, _ = pairing.Encode(p)
	if err = runPairImport(path, []string{"--text", code}); err != nil {
		t.Fatal("key rotation must replace the same device", err)
	}
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &local); err != nil {
		t.Fatal(err)
	}
	if local["proxy"] != "direct" || local["token"] != p.Token {
		t.Fatal("rotation lost local proxy or new token")
	}
	p.Device = "dev-second"
	code, _ = pairing.Encode(p)
	if err = runPairImport(path, []string{"--text", code}); err == nil {
		t.Fatal("different device overwrote profile without consent")
	}
	if err = runPairImport(path, []string{"--text", code, "--force"}); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	local = nil
	if err = json.Unmarshal(b, &local); err != nil {
		t.Fatal(err)
	}
	if local["proxy"] != nil {
		t.Fatal("other device inherited local proxy")
	}
}
