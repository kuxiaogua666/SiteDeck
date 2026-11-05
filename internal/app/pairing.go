package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
	"sitedeck/internal/agentconfig"
	"sitedeck/internal/client"
	"sitedeck/internal/pairing"
)

func readPairingText(value string) (string, error) {
	if value != "-" {
		return value, nil
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "请粘贴完整配对文本（不回显），然后回车：")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if len(b) > pairing.MaxText {
			return "", fmt.Errorf("配对文本过长")
		}
		return string(b), err
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, pairing.MaxText+1))
	if err != nil {
		return "", err
	}
	if len(data) > pairing.MaxText {
		return "", fmt.Errorf("配对文本过长")
	}
	return strings.TrimSpace(string(data)), nil
}
func pairingConfig(text string) (client.Config, error) {
	text, err := readPairingText(text)
	if err != nil {
		return client.Config{}, err
	}
	p, err := pairing.Decode(text)
	if err != nil {
		return client.Config{}, err
	}
	return client.Config{Hub: p.Hub, Device: p.Device, Fingerprint: p.Fingerprint, Token: p.Token, Transport: p.Transport}, nil
}
func runPairImport(profile string, args []string) error {
	fs := flag.NewFlagSet("site-cli pair", flag.ContinueOnError)
	text := fs.String("text", "-", "pairing text, or - to read from stdin without shell history")
	output := fs.String("output", profile, "local private profile to save")
	force := fs.Bool("force", false, "replace a profile belonging to a different device")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	value, err := readPairingText(*text)
	if err != nil {
		return err
	}
	p, err := pairing.Decode(value)
	if err != nil {
		return err
	}
	saved := struct {
		pairing.Profile
		Proxy string `json:"proxy,omitempty"`
	}{Profile: p}
	if old, err := os.ReadFile(*output); err == nil {
		var existing struct {
			pairing.Profile
			Proxy string `json:"proxy,omitempty"`
		}
		valid := json.Unmarshal(old, &existing) == nil
		if (!valid || existing.Device != p.Device) && !*force {
			return fmt.Errorf("目标配置已属于其他设备，请指定 --output 新路径或 --force")
		}
		if valid && existing.Device == p.Device {
			saved.Proxy = existing.Proxy
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	if err = agentconfig.WritePrivate(*output, append(data, '\n')); err != nil {
		return err
	}
	emit(map[string]any{"imported": true, "deviceId": p.Device, "fingerprint": p.Fingerprint, "profileFile": *output, "guestExpiresAt": p.Expires, "next": "site-cli doctor 或 site-cli host shell"})
	return nil
}
func printProfileText(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var p pairing.Profile
	if json.Unmarshal(data, &p) != nil {
		return fmt.Errorf("配对文件格式无效")
	}
	text, err := pairing.Encode(p)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, text)
	return nil
}
