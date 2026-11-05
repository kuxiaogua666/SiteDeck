package app

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/term"
	"sitedeck/internal/agentconfig"
	"sitedeck/internal/identity"
	"sitedeck/internal/pairing"
)

func agentDirectory(value string) (string, error) {
	if value != "" {
		return filepath.Abs(value)
	}
	return agentconfig.DefaultDirectory()
}

func runAgentDefault(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return runAgent(ctx, append([]string{"run"}, args...))
	}
	directory, err := agentDirectory("")
	if err != nil {
		return err
	}
	config, err := agentconfig.Discover(directory)
	if err != nil {
		return err
	}
	if config == "" {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("未找到 site-agent.json；请将配置放在程序旁，或先运行 site-agent setup --hub URL --registration-file hub.key --allow host")
		}
		config, err = configureAgent(nil)
		if err != nil {
			return err
		}
	} else {
		settings, loadErr := agentconfig.LoadSettings(config)
		if loadErr != nil {
			return loadErr
		}
		if settings.RemoteEnabled && (settings.Hub == "" || settings.RegistrationToken == "") {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return fmt.Errorf("Agent 配置缺少 Hub 地址或注册凭据，请运行 site-agent setup --config %s", config)
			}
			config, err = configureAgent([]string{"--config", config})
			if err != nil {
				return err
			}
		}
	}
	return runAgent(ctx, []string{"run", "--config", config})
}

// configureAgent saves only deployment settings. Device identity is never part
// of the portable file, so copying it cannot clone the paired device.
func configureAgent(args []string) (string, error) {
	fs := flag.NewFlagSet("site-agent setup", flag.ContinueOnError)
	config := fs.String("config", "", "settings file to create or update")
	directory := fs.String("data-dir", "", "per-device private data directory")
	hub := fs.String("hub", "", "Hub WebSocket endpoint")
	registration := fs.String("registration-file", "", "read registration credential from hub.key")
	name := fs.String("name", "", "device display name")
	allow := fs.String("allow", "", "local capabilities: host,ssh,serial")
	ttl := fs.Int("guest-ttl-hours", 24, "guest credential lifetime in hours")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 {
		return "", fmt.Errorf("unexpected arguments")
	}
	dataDir, err := agentDirectory(*directory)
	if err != nil {
		return "", err
	}
	explicit := *config != ""
	if *config == "" {
		*config, err = agentconfig.Discover(dataDir)
		if err != nil {
			return "", err
		}
	}
	if *config == "" {
		executable, err := os.Executable()
		if err != nil {
			return "", err
		}
		*config = filepath.Join(filepath.Dir(executable), agentconfig.PortableName)
	}
	settings, err := agentconfig.LoadSettings(*config)
	if os.IsNotExist(err) {
		settings = agentconfig.DefaultSettings()
		settings.AutoStart = true
	} else if err != nil {
		return "", err
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	reader := bufio.NewReader(os.Stdin)
	prompt := func(label, initial string, secret bool) (string, error) {
		if !interactive {
			return "", fmt.Errorf("缺少 %s；请使用 setup 的参数，或在交互终端输入", label)
		}
		if initial != "" {
			fmt.Fprintf(os.Stderr, "%s [%s]: ", label, initial)
		} else {
			fmt.Fprintf(os.Stderr, "%s: ", label)
		}
		var value string
		if secret {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return "", err
			}
			value = string(b)
		} else {
			line, err := reader.ReadString('\n')
			if err != nil {
				return "", err
			}
			value = line
		}
		value = strings.TrimSpace(value)
		if value == "" {
			value = initial
		}
		return value, nil
	}
	if visitedFlag(fs, "hub") {
		settings.Hub = strings.TrimSpace(*hub)
	} else if settings.Hub == "" {
		settings.Hub, err = prompt("Hub WebSocket", settings.Hub, false)
		if err != nil {
			return "", err
		}
	}
	if *registration != "" {
		settings.RegistrationToken, err = identity.ReadSecret(*registration)
		if err != nil {
			return "", err
		}
	} else if settings.RegistrationToken == "" {
		settings.RegistrationToken, err = prompt("Hub 注册凭据（粘贴 hub.key 内容，不回显）", "", true)
		if err != nil {
			return "", err
		}
	}
	if visitedFlag(fs, "name") {
		settings.Name = *name
	}
	if visitedFlag(fs, "allow") {
		settings.Permissions = parsePermissions(*allow)
	} else if interactive {
		value, err := prompt("允许的能力 host,ssh,serial（开启后可远程操作）", strings.Join(settings.Permissions, ","), false)
		if err != nil {
			return "", err
		}
		settings.Permissions = parsePermissions(value)
	}
	if visitedFlag(fs, "guest-ttl-hours") {
		settings.GuestTTLHours = *ttl
	}
	settings.RemoteEnabled = true
	if settings.Hub == "" || settings.RegistrationToken == "" {
		return "", fmt.Errorf("Hub 地址和注册凭据不能为空")
	}
	if err = agentconfig.Save(*config, settings); err != nil {
		if explicit || (!os.IsPermission(err) && !errors.Is(err, syscall.EROFS)) {
			return "", err
		}
		*config = filepath.Join(dataDir, agentconfig.PortableName)
		if err = agentconfig.Save(*config, settings); err != nil {
			return "", err
		}
	}
	path, err := filepath.Abs(*config)
	if err != nil {
		return "", err
	}
	emit(map[string]any{"configured": true, "configFile": path, "dataDirectory": dataDir, "permissions": settings.Permissions})
	return path, nil
}
func parsePermissions(value string) []string {
	result := []string{}
	for _, v := range strings.Split(value, ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			result = append(result, v)
		}
	}
	return result
}

func pairingTextForState(state *identity.State, hub string) (string, error) {
	return pairing.Encode(pairing.Profile{Hub: hub, Device: state.Device, Fingerprint: state.Fingerprint(), Token: state.GuestToken, Expires: state.GuestExpiresAt})
}
func emitPairing(state *identity.State, hub string, plain bool) error {
	text, err := pairingTextForState(state, hub)
	if err != nil {
		return err
	}
	if plain {
		fmt.Fprintln(os.Stdout, text)
	} else {
		emit(map[string]any{"deviceId": state.Device, "pairingText": text, "guestExpiresAt": state.GuestExpiresAt})
	}
	return nil
}
