package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/term"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sitedeck/internal/agentconfig"
	"strings"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"
	"sitedeck/internal/agent"
	"sitedeck/internal/hub"
	"sitedeck/internal/identity"
	"sitedeck/internal/protocol"
)

func Run(role string, args []string) int {
	log.SetFlags(log.LstdFlags | log.LUTC)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var err error
	switch role {
	case "hub":
		err = runHub(ctx, args)
	case "agent":
		err = runAgent(ctx, args)
	case "cli":
		return runCLI(ctx, args)
	default:
		err = fmt.Errorf("unknown role")
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return emitError(err, 1)
	}
	return 0
}
func emit(value any) { _ = json.NewEncoder(os.Stdout).Encode(value) }
func emitError(err error, code int) int {
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) {
		rpcErr = protocol.Fail("ERROR", err.Error())
	}
	emit(map[string]any{"error": rpcErr})
	return code
}
func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func loadICE(path string) ([]webrtc.ICEServer, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ice []webrtc.ICEServer
	err = json.Unmarshal(b, &ice)
	return ice, err
}
func runHub(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: site-hub init|serve [options]")
	}
	fs := flag.NewFlagSet("site-hub "+args[0], flag.ContinueOnError)
	key := fs.String("registration-file", ".local/hub.key", "protected Agent registration credential file")
	listen := fs.String("listen", protocol.DefaultHubAddress, "listen address")
	adminFile := fs.String("admin-file", ".local/hub-admin.key", "independent management login secret file")
	stateFile := fs.String("state", ".local/hub-state.json", "persistent device names and access grants")
	cert := fs.String("tls-cert", "", "TLS certificate file (required for remote connections)")
	tlsKey := fs.String("tls-key", "", "TLS private key file")
	channels := fs.Int("max-channels", 8, "maximum channels per device")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if args[0] == "init" {
		for _, path := range []string{*key, *adminFile} {
			if _, err := identity.ReadSecret(path); err != nil {
				if !os.IsNotExist(err) {
					return err
				}
				if err = writePrivate(path, []byte(identity.Random(32)+"\n")); err != nil {
					return err
				}
			}
		}
		registration, err := identity.ReadSecret(*key)
		if err != nil {
			return err
		}
		admin, err := identity.ReadSecret(*adminFile)
		if err != nil {
			return err
		}
		if _, err = hub.Open(registration, admin, *stateFile); err != nil {
			return err
		}
		emit(map[string]string{"registrationFile": *key, "adminFile": *adminFile, "stateFile": *stateFile, "managementURL": "http://" + *listen + "/admin/"})
		return nil
	}
	if args[0] != "serve" {
		return fmt.Errorf("unknown Hub command")
	}
	secret, err := identity.ReadSecret(*key)
	if err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if *cert == "" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("a non-loopback listener requires --tls-cert and --tls-key")
	}
	if (*cert == "") != (*tlsKey == "") {
		return fmt.Errorf("both TLS files are required")
	}
	if *channels < 1 || *channels > 64 {
		return fmt.Errorf("max-channels must be 1..64")
	}
	adminSecret, err := identity.ReadSecret(*adminFile)
	if err != nil {
		return fmt.Errorf("read management secret (run site-hub init first): %w", err)
	}
	h, err := hub.Open(secret, adminSecret, *stateFile)
	if err != nil {
		return err
	}
	defer h.Close()
	h.MaxChannels = *channels
	server := &http.Server{Addr: *listen, Handler: h.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8192}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("Hub listening address=%s tls=%t", *listen, *cert != "")
	if *cert != "" {
		err = server.ListenAndServeTLS(*cert, *tlsKey)
	} else {
		err = server.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func runAgent(ctx context.Context, args []string) error {
	if len(args) == 0 && runtime.GOOS == "windows" {
		return runAgentUI(ctx, nil)
	}
	if len(args) > 0 && (args[0] == "ui" || args[0] == "gui") {
		return runAgentUI(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "desktop" {
		return runDesktop(args[1:])
	}
	if len(args) == 0 {
		return runAgentDefault(ctx, nil)
	}
	if args[0] == "setup" {
		_, err := configureAgent(args[1:])
		return err
	}
	if args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stdout, "site-agent [ui|setup|run|init|info|rotate|pair] [options]; default: auto-load site-agent.json; --headless uses CLI on Windows")
		return nil
	}
	if args[0] == "--headless" {
		return runAgentDefault(ctx, args[1:])
	}
	if strings.HasPrefix(args[0], "-") {
		return runAgentDefault(ctx, args)
	}
	fs := flag.NewFlagSet("site-agent "+args[0], flag.ContinueOnError)
	dataDirectory := fs.String("data-dir", "", "per-device private data directory")
	printPair := fs.Bool("print-pairing", false, "print secret pairing text even when stdout is redirected")
	plainPair := fs.Bool("plain", false, "print only the pairing text with the pair command")
	configFile := fs.String("config", "", "Agent UI configuration file; share settings and identity with the UI")
	localOnly := fs.Bool("local-only", false, "run without Hub")
	stateFile := fs.String("state", ".local/agent.json", "protected identity state file")
	endpoint := fs.String("hub", protocol.DefaultHubURL, "Hub endpoint")
	name := fs.String("name", "", "initial device display name; later editable in Hub management")
	pairFile := fs.String("pair-file", ".local/pair.json", "protected CLI pairing profile")
	registration := fs.String("registration-file", ".local/hub.key", "protected Hub registration credential file")
	allow := fs.String("allow", "", "comma-separated local permissions: host,ssh,serial")
	iceFile := fs.String("ice-file", "", "JSON array of ICE servers, with TURN credentials if needed")
	guestTTL := fs.Duration("guest-ttl", 24*time.Hour, "guest access lifetime used by init and rotate (1m..720h)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	dataDir, err := agentDirectory(*dataDirectory)
	if err != nil {
		return err
	}
	if *configFile == "" {
		*configFile, err = agentconfig.Discover(dataDir)
		if err != nil {
			return err
		}
	}

	if visitedFlag(fs, "data-dir") && !visitedFlag(fs, "state") {
		*stateFile = filepath.Join(dataDir, "agent.json")
	}
	registrationToken := ""
	if *configFile != "" {
		settings, err := agentconfig.LoadSettings(*configFile)
		if err != nil {
			return err
		}
		if !visitedFlag(fs, "state") {
			if visitedFlag(fs, "data-dir") || filepath.Base(*configFile) == agentconfig.PortableName {
				*stateFile = filepath.Join(dataDir, "agent.json")
			} else {
				*stateFile = filepath.Join(filepath.Dir(*configFile), "agent.json")
			}
		}
		if !visitedFlag(fs, "hub") {
			*endpoint = settings.Hub
		}
		if !visitedFlag(fs, "name") {
			*name = settings.Name
		}
		if !visitedFlag(fs, "allow") {
			*allow = strings.Join(settings.Permissions, ",")
		}
		if !visitedFlag(fs, "ice-file") {
			*iceFile = settings.ICEFile
		}
		if !visitedFlag(fs, "guest-ttl") {
			*guestTTL = time.Duration(settings.GuestTTLHours) * time.Hour
		}
		if !visitedFlag(fs, "local-only") {
			*localOnly = !settings.RemoteEnabled
		}
		if !visitedFlag(fs, "registration-file") {
			registrationToken = settings.RegistrationToken
			if !*localOnly && registrationToken == "" {
				return fmt.Errorf("Agent 配置缺少注册凭据，请通过 UI 或 site-agent setup 保存 hub.key 内容")
			}
		}
	}

	if *guestTTL < time.Minute || *guestTTL > 30*24*time.Hour {
		return fmt.Errorf("guest-ttl must be 1m..720h")
	}
	for _, scope := range strings.Split(*allow, ",") {
		scope = strings.TrimSpace(scope)
		if scope != "" && scope != "host" && scope != "ssh" && scope != "serial" {
			return fmt.Errorf("unknown permission %q", scope)
		}
	}
	var state *identity.State
	if args[0] == "init" {
		state, err = identity.Init(*stateFile, *guestTTL)
	} else {
		state, err = identity.Load(*stateFile)
		if os.IsNotExist(err) && args[0] == "run" {
			state, err = identity.Init(*stateFile, *guestTTL)
		}
	}
	if err != nil {
		return err
	}
	switch args[0] {
	case "init":
		if *name != "" {
			if len(*name) > 240 || strings.ContainsAny(*name, "\r\n\x00") {
				return fmt.Errorf("invalid device name")
			}
			state.Name = strings.TrimSpace(*name)
			if err = state.Save(*stateFile); err != nil {
				return err
			}
		}
		if state.GuestExpiresAt.IsZero() {
			state.GuestExpiresAt = time.Now().Add(*guestTTL)
			if err = state.Save(*stateFile); err != nil {
				return err
			}
		}
		profile := map[string]any{"hub": *endpoint, "deviceId": state.Device, "fingerprint": state.Fingerprint(), "token": state.GuestToken, "transport": "auto", "guestExpiresAt": state.GuestExpiresAt}
		b, _ := json.MarshalIndent(profile, "", "  ")
		if err = writePrivate(*pairFile, append(b, '\n')); err != nil {
			return err
		}
		emit(map[string]any{"deviceId": state.Device, "fingerprint": state.Fingerprint(), "pairFile": *pairFile, "stateFile": *stateFile, "guestExpiresAt": state.GuestExpiresAt})
		return nil
	case "info":
		emit(map[string]any{"deviceId": state.Device, "fingerprint": state.Fingerprint(), "version": protocol.Version, "guestExpiresAt": state.GuestExpiresAt})
		return nil
	case "rotate":
		state.GuestToken = identity.Random(32)
		state.GuestExpiresAt = time.Now().Add(*guestTTL)
		if err = state.Save(*stateFile); err != nil {
			return err
		}
		// Restart revokes existing transports. A running Agent watches state below.
		result := map[string]any{"rotated": true, "deviceId": state.Device, "pairing": "run site-agent pair --plain to obtain the new pairing text"}
		if *printPair || term.IsTerminal(int(os.Stdout.Fd())) {
			text, err := pairingTextForState(state, *endpoint)
			if err != nil {
				return err
			}
			result["pairingText"] = text
		}
		emit(result)
		return nil
	case "pair":
		return emitPairing(state, *endpoint, *plainPair)
	case "run":
		if !*localOnly && (*printPair || term.IsTerminal(int(os.Stdout.Fd()))) {
			if err := emitPairing(state, *endpoint, false); err != nil {
				return err
			}
		}
		return agent.RunStored(ctx, agent.RuntimeOptions{StateFile: *stateFile, RegistrationFile: *registration, Registration: registrationToken, Hub: *endpoint, Name: *name, Allow: *allow, ICEFile: *iceFile, LocalOnly: *localOnly})

	default:
		return fmt.Errorf("unknown Agent command")
	}
}

func readInput(path string) (map[string]any, error) {
	var reader io.Reader = os.Stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		reader = f
	}
	b, err := io.ReadAll(io.LimitReader(reader, 96<<10+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 96<<10 {
		return nil, fmt.Errorf("input exceeds 96 KiB")
	}
	var value map[string]any
	if err = json.Unmarshal(b, &value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("input must be a JSON object")
	}
	return value, nil
}
