package agentconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"sitedeck/internal/transport"
)

type Settings struct {
	Version           int      `json:"version"`
	Hub               string   `json:"hub"`
	Name              string   `json:"name"`
	RemoteEnabled     bool     `json:"remoteEnabled"`
	Permissions       []string `json:"permissions"`
	AutoStart         bool     `json:"autoStart"`
	GuestTTLHours     int      `json:"guestTTLHours"`
	RegistrationToken string   `json:"registrationToken,omitempty"`
	ICEFile           string   `json:"iceFile,omitempty"`
}
type SettingsView struct {
	Hub                    string   `json:"hub"`
	Name                   string   `json:"name"`
	RemoteEnabled          bool     `json:"remoteEnabled"`
	Permissions            []string `json:"permissions"`
	AutoStart              bool     `json:"autoStart"`
	GuestTTLHours          int      `json:"guestTTLHours"`
	RegistrationConfigured bool     `json:"registrationConfigured"`
	ICEFile                string   `json:"iceFile,omitempty"`
}

func DefaultDirectory() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		executable, execErr := os.Executable()
		if execErr != nil {
			return "", execErr
		}
		return filepath.Join(filepath.Dir(executable), ".site-agent"), nil
	}
	return configuredDirectory(dir)
}

// configuredDirectory reuses an existing identity without copying it. Keeping
// the legacy directory also keeps the same process lock during mixed upgrades.
func configuredDirectory(base string) (string, error) {
	current := filepath.Join(base, "SiteDeck", "Agent")
	legacy := filepath.Join(base, "OpenEndlesst", "Agent")
	for _, directory := range []string{current, legacy} {
		info, err := os.Stat(filepath.Join(directory, "agent.json"))
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("Agent identity is not a regular file")
			}
			return directory, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
	}
	// Preserve saved settings from an installation which has not initialized its
	// identity yet. A new install always uses the SiteDeck directory.
	for _, name := range []string{PortableName, "agent-ui.json", "ui-runtime.json"} {
		if _, err := os.Stat(filepath.Join(legacy, name)); err == nil {
			return legacy, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return current, nil
}

func DefaultSettings() Settings {
	name, _ := os.Hostname()
	return Settings{Version: 1, Name: name, RemoteEnabled: true, Permissions: []string{}, GuestTTLHours: 24}
}
func LoadSettings(path string) (Settings, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, err
	}
	if len(b) > 64<<10 {
		return Settings{}, fmt.Errorf("Agent 界面配置超过大小限制")
	}
	settings := DefaultSettings()
	if err = json.Unmarshal(b, &settings); err != nil {
		return settings, fmt.Errorf("Agent 界面配置无效")
	}
	return settings, ValidateSettings(settings)
}
func ValidateSettings(s Settings) error {
	if s.Version != 1 {
		return fmt.Errorf("不支持的 Agent 配置版本")
	}
	if strings.TrimSpace(s.Name) == "" || !utf8.ValidString(s.Name) || utf8.RuneCountInString(s.Name) > 80 {
		return fmt.Errorf("设备名称需要 1–80 个字符")
	}
	for _, r := range s.Name {
		if unicode.IsControl(r) {
			return fmt.Errorf("设备名称不能包含控制字符")
		}
	}
	if s.Hub != "" {
		if err := transport.ValidateEndpoint(s.Hub); err != nil {
			return fmt.Errorf("Hub 地址无效：跨机器连接请使用 wss:// 地址，本机可使用 ws://127.0.0.1")
		}
	}
	if len(s.Permissions) > 3 {
		return fmt.Errorf("权限配置无效")
	}
	seen := map[string]bool{}
	for _, scope := range s.Permissions {
		if (scope != "host" && scope != "ssh" && scope != "serial") || seen[scope] {
			return fmt.Errorf("权限配置无效")
		}
		seen[scope] = true
	}
	if s.GuestTTLHours < 1 || s.GuestTTLHours > 720 {
		return fmt.Errorf("访客授权时长必须为 1–720 小时")
	}
	if s.RegistrationToken != "" {
		if len(s.RegistrationToken) < 32 || len(s.RegistrationToken) > 4096 {
			return fmt.Errorf("Hub 注册凭据长度无效")
		}
		for _, r := range s.RegistrationToken {
			if r < 33 || r > 126 {
				return fmt.Errorf("注册凭据必须为单行文本，请选择 hub.key 文件")
			}
		}
	}
	return nil
}
func (s Settings) View() SettingsView {
	return SettingsView{Hub: s.Hub, Name: s.Name, RemoteEnabled: s.RemoteEnabled, Permissions: append([]string{}, s.Permissions...), AutoStart: s.AutoStart, GuestTTLHours: s.GuestTTLHours, RegistrationConfigured: s.RegistrationToken != "", ICEFile: s.ICEFile}
}
func WritePrivate(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".agent-ui-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func Marshal(settings Settings) ([]byte, error) {
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
