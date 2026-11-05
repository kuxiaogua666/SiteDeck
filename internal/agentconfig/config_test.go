package agentconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMinimalPortableConfigAndDiscovery(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "binary", "site-agent")
	directory := filepath.Join(root, "data")
	os.MkdirAll(filepath.Dir(exe), 0700)
	os.MkdirAll(directory, 0700)
	legacy := filepath.Join(directory, "agent-ui.json")
	if err := os.WriteFile(legacy, []byte(`{"hub":"wss://legacy.test/ws"}`), 0600); err != nil {
		t.Fatal(err)
	}
	path, err := Find(exe, directory)
	if err != nil || path != legacy {
		t.Fatal(path, err)
	}
	portable := filepath.Join(filepath.Dir(exe), PortableName)
	if err = os.WriteFile(portable, []byte(`{"hub":"wss://new.test/ws","registrationToken":"`+strings.Repeat("a", 64)+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	path, err = Find(exe, directory)
	if err != nil || path != portable {
		t.Fatal("portable config must take precedence", path, err)
	}
	settings, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.RemoteEnabled || settings.Version != 1 || settings.GuestTTLHours != 24 || settings.Hub != "wss://new.test/ws" {
		t.Fatal("missing fields did not get safe defaults")
	}
	if len(settings.Permissions) != 0 {
		t.Fatal("permissions must default to disabled")
	}
	settings.Permissions = []string{"host"}
	if err = Save(path, settings); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatal("configuration not private")
		}
	}
}

func TestSiteDeckDirectoryPreservesLegacyIdentity(t *testing.T) {
	base := t.TempDir()
	current := filepath.Join(base, "SiteDeck", "Agent")
	legacy := filepath.Join(base, "OpenEndlesst", "Agent")
	if dir, err := configuredDirectory(base); err != nil || dir != current {
		t.Fatal("new install path", dir, err)
	}
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "agent.json"), []byte("existing identity bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(current, 0700); err != nil {
		t.Fatal(err)
	}
	// An empty new-brand directory must never shadow the existing identity.
	if dir, err := configuredDirectory(base); err != nil || dir != legacy {
		t.Fatal("legacy identity not reused", dir, err)
	}
	contents, err := os.ReadFile(filepath.Join(legacy, "agent.json"))
	if err != nil || string(contents) != "existing identity bytes" {
		t.Fatal("legacy identity modified")
	}
	if err = os.WriteFile(filepath.Join(current, "agent.json"), []byte("explicit new identity"), 0600); err != nil {
		t.Fatal(err)
	}
	if dir, err := configuredDirectory(base); err != nil || dir != current {
		t.Fatal("explicit current identity not respected", dir, err)
	}
}

func TestSiteDeckDirectoryPreservesLegacySettings(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "OpenEndlesst", "Agent")
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "site-agent.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if dir, err := configuredDirectory(base); err != nil || dir != legacy {
		t.Fatal("legacy settings not reused", dir, err)
	}
}
