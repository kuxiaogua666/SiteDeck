package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
	"sitedeck/internal/client"
	"sitedeck/internal/protocol"
	"sitedeck/internal/rpc"
)

type caller interface {
	Call(context.Context, string, any) (json.RawMessage, *protocol.Error)
}
type namedCaller struct {
	client *rpc.Client
	id     string
}

func (c namedCaller) Call(ctx context.Context, method string, params any) (json.RawMessage, *protocol.Error) {
	return c.client.CallAs(ctx, method, params, c.id)
}

type envFlags []string

func (e *envFlags) String() string     { return strings.Join(*e, ",") }
func (e *envFlags) Set(v string) error { *e = append(*e, v); return nil }

func runCLI(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("site-cli", flag.ContinueOnError)
	pairText := fs.String("pair-text", "", "connect directly using pairing text; - reads stdin privately")
	profile := fs.String("profile", ".local/pair.json", "protected device pairing profile")
	endpoint := fs.String("hub", "", "override Hub endpoint")
	device := fs.String("device", "", "override target device")
	pin := fs.String("fingerprint", "", "override paired Agent fingerprint")
	tokenFile := fs.String("token-file", "", "read guest credential from protected file")
	accessFile := fs.String("access-file", "", "Hub access grant JSON or token file")
	proxy := fs.String("proxy", "", "Hub proxy mode: env (default) or direct")
	policy := fs.String("transport", "", "auto, p2p or relay")
	iceFile := fs.String("ice-file", "", "ICE server JSON file")
	iceTimeout := fs.Duration("ice-timeout", 8*time.Second, "deadline for each ICE attempt")
	timeout := fs.Duration("timeout", 30*time.Second, "RPC deadline, maximum 5m")
	daemonFile := fs.String("daemon-file", "", "use a local daemon descriptor")
	clientID := fs.String("client-id", "", "stable logical member ID for automation through a daemon")
	jsonMode := fs.Bool("json", false, "JSON output (also disables interactive terminal mode)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return emitError(err, 2)
	}
	remaining := fs.Args()
	if len(*clientID) > 64 {
		return emitError(fmt.Errorf("client-id must contain at most 64 bytes"), 2)
	}
	if len(remaining) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: site-cli [connection options] pair | pair-text | doctor | rpc METHOD --input FILE | host exec --command COMMAND | host shell | ssh ... | serial ... | session ... | daemon ...")
		return 2
	}
	if remaining[0] == "pair" {
		if err := runPairImport(*profile, remaining[1:]); err != nil {
			return emitError(err, 2)
		}
		return 0
	}
	if remaining[0] == "pair-text" {
		if len(remaining) != 1 {
			return emitError(fmt.Errorf("unexpected arguments"), 2)
		}
		if err := printProfileText(*profile); err != nil {
			return emitError(err, 2)
		}
		return 0
	}
	if remaining[0] == "version" {
		emit(map[string]any{"version": protocol.Version, "protocolVersion": 1})
		return 0
	}
	if *timeout <= 0 || *timeout > 5*time.Minute {
		return emitError(fmt.Errorf("timeout must be greater than zero and at most 5m"), 2)
	}
	if *iceTimeout < time.Second || *iceTimeout > time.Minute {
		return emitError(fmt.Errorf("ice-timeout must be 1s..1m"), 2)
	}
	method := remaining[0]
	remaining = remaining[1:]
	if method == "rpc" {
		if len(remaining) == 0 {
			return emitError(fmt.Errorf("RPC method is required"), 2)
		}
		method = remaining[0]
		remaining = remaining[1:]
	} else if method != "doctor" {
		if len(remaining) == 0 {
			return emitError(fmt.Errorf("subcommand is required"), 2)
		}
		method += "." + remaining[0]
		remaining = remaining[1:]
	}
	options := flag.NewFlagSet(method, flag.ContinueOnError)
	input := options.String("input", "", "JSON request file, or - for stdin")
	output := options.String("output", "", "artifact destination or daemon descriptor path")
	options.BoolVar(jsonMode, "json", *jsonMode, "JSON output")
	params := map[string]any{}
	stringOptions := map[string]string{"command": "command", "cwd": "cwd", "shell": "shell", "id": "id", "cursor": "cursor", "text": "text", "data": "data", "pattern": "pattern", "port": "port", "host": "host", "user": "user", "fingerprint": "fingerprint", "parity": "parity", "source": "source"}
	str := map[string]*string{}
	for name := range stringOptions {
		str[name] = options.String(name, "", name)
	}
	intOptions := map[string]string{"baud": "baudRate", "data-bits": "dataBits", "stop-bits": "stopBits", "ssh-port": "port", "rows": "rows", "cols": "cols", "limit": "limit", "wait-ms": "waitMs", "offset": "offset"}
	ints := map[string]*int{}
	for name := range intOptions {
		ints[name] = options.Int(name, 0, name)
	}
	readOnly := options.Bool("read-only", false, "attach without input")
	regex := options.Bool("regex", false, "Go RE2 regular expression (no lookaround or backreferences)")
	var env envFlags
	options.Var(&env, "env", "additional environment KEY=VALUE; repeatable")
	if err := options.Parse(remaining); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return emitError(err, 2)
	}
	if *input != "" {
		var err error
		params, err = readInput(*input)
		if err != nil {
			return emitError(err, 2)
		}
	}
	options.Visit(func(f *flag.Flag) {
		if key, ok := stringOptions[f.Name]; ok {
			params[key] = *str[f.Name]
		}
		if key, ok := intOptions[f.Name]; ok {
			params[key] = *ints[f.Name]
		}
	})
	if len(env) > 0 {
		values := map[string]string{}
		for _, v := range env {
			parts := strings.SplitN(v, "=", 2)
			if len(parts) != 2 {
				return emitError(fmt.Errorf("env must be KEY=VALUE"), 2)
			}
			values[parts[0]] = parts[1]
		}
		params["env"] = values
	}
	if *readOnly {
		params["readOnly"] = true
	}
	if *regex {
		params["regex"] = true
	}
	if options.NArg() > 0 {
		if method == "host.exec" {
			params["argv"] = options.Args()
		} else {
			return emitError(fmt.Errorf("unexpected positional arguments"), 2)
		}
	}
	if method == "serial.watch" {
		method = "serial.read"
		params["waitMs"] = 1000
		params["watch"] = true
	}
	if method == "daemon.status" || method == "daemon.stop" {
		if *daemonFile == "" {
			return emitError(fmt.Errorf("--daemon-file is required"), 2)
		}
		value, err := daemonControl(ctx, *daemonFile, strings.TrimPrefix(method, "daemon."))
		if err != nil {
			return emitError(err, 3)
		}
		emit(value)
		return 0
	}
	var cfg client.Config
	var err error
	if *daemonFile == "" || method == "daemon.serve" {
		if *pairText != "" {
			if visitedFlag(fs, "profile") {
				return emitError(fmt.Errorf("choose --profile or --pair-text"), 2)
			}
			if *pairText == "-" && *input == "-" {
				return emitError(fmt.Errorf("pairing text and RPC input cannot both use stdin"), 2)
			}
			var err error
			cfg, err = pairingConfig(*pairText)
			if err != nil {
				return emitError(err, 2)
			}
		} else {
			b, err := os.ReadFile(*profile)
			if err != nil {
				return emitError(err, 2)
			}
			if err = json.Unmarshal(b, &cfg); err != nil {
				return emitError(err, 2)
			}
		}
		if *endpoint != "" {
			cfg.Hub = *endpoint
		}
		if *device != "" {
			cfg.Device = *device
		}
		if *pin != "" {
			cfg.Fingerprint = *pin
		}
		if *policy != "" {
			cfg.Transport = *policy
		}
		if visitedFlag(fs, "proxy") {
			cfg.Proxy = *proxy
		}
		if cfg.Proxy != "" && cfg.Proxy != "env" && cfg.Proxy != "direct" {
			return emitError(fmt.Errorf("proxy must be env or direct"), 2)
		}
		cfg.ICETimeout = *iceTimeout
		if *tokenFile != "" {
			cfg.Token, err = identitySecret(*tokenFile)
			if err != nil {
				return emitError(err, 2)
			}
		}
		if *accessFile != "" {
			b, err := os.ReadFile(*accessFile)
			if err != nil {
				return emitError(err, 2)
			}
			if len(b) > 4096 {
				return emitError(fmt.Errorf("access file exceeds 4 KiB"), 2)
			}
			var grant struct {
				Token  string `json:"accessToken"`
				Device string `json:"deviceId"`
			}
			if strings.HasPrefix(strings.TrimSpace(string(b)), "{") {
				if err = json.Unmarshal(b, &grant); err != nil {
					return emitError(fmt.Errorf("invalid access grant JSON"), 2)
				}
				if grant.Device != "" && grant.Device != cfg.Device {
					return emitError(fmt.Errorf("access grant belongs to another device"), 2)
				}
				cfg.AccessToken = grant.Token
			} else {
				cfg.AccessToken = strings.TrimSpace(string(b))
			}
			if len(cfg.AccessToken) < 32 {
				return emitError(fmt.Errorf("invalid Hub access token"), 2)
			}
		}
		if *iceFile != "" {
			cfg.ICE, err = loadICE(*iceFile)
			if err != nil {
				return emitError(err, 2)
			}
		}
	}
	if method == "daemon.serve" {
		if *output == "" {
			return emitError(fmt.Errorf("daemon serve requires --output descriptor.json"), 2)
		}
		if err := serveDaemon(ctx, cfg, *output); err != nil {
			return emitError(err, 3)
		}
		return 0
	}
	if *daemonFile != "" && *accessFile != "" {
		return emitError(fmt.Errorf("provide --access-file when starting daemon serve; an existing channel cannot change its grant"), 2)
	}
	if *daemonFile != "" && visitedFlag(fs, "proxy") {
		return emitError(fmt.Errorf("provide --proxy when starting daemon serve; an existing channel cannot change its proxy"), 2)
	}
	var api caller
	if *daemonFile != "" {
		local, err := newDaemonClient(*daemonFile)
		if err != nil {
			return emitError(err, 3)
		}
		api = local
		if *clientID != "" {
			local.clientID = *clientID
		}
	} else {
		conn, err := client.Open(ctx, cfg)
		if err != nil {
			return emitError(err, 3)
		}
		defer conn.Close()
		api = conn.RPC
		if *clientID != "" {
			api = namedCaller{client: conn.RPC, id: *clientID}
		}
	}
	requestCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if method == "artifact.download" {
		id, _ := params["id"].(string)
		if id == "" || *output == "" {
			return emitError(fmt.Errorf("artifact download requires --id and --output"), 2)
		}
		path, err := download(requestCtx, api, id, *output, "")
		if err != nil {
			return emitError(err, 1)
		}
		emit(map[string]any{"artifactPath": path})
		return 0
	}
	result, rpcErr := api.Call(requestCtx, method, params)
	if rpcErr != nil {
		return emitError(rpcErr, 1)
	}
	if method == "host.shell" || method == "ssh.shell" || method == "session.attach" {
		if !*jsonMode && term.IsTerminal(int(os.Stdin.Fd())) {
			var opened struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(result, &opened); err != nil {
				return emitError(err, 1)
			}
			if err := interactive(ctx, api, opened.ID, *readOnly); err != nil {
				return emitError(err, 1)
			}
			return 0
		}
	}
	if watch, _ := params["watch"].(bool); watch {
		for {
			var chunk struct {
				Data []byte `json:"data"`
				Next string `json:"nextCursor"`
				EOF  bool   `json:"eof"`
			}
			if err := json.Unmarshal(result, &chunk); err != nil {
				return emitError(err, 1)
			}
			if *jsonMode {
				emit(json.RawMessage(result))
			} else {
				_, _ = os.Stdout.Write(chunk.Data)
			}
			if chunk.EOF || ctx.Err() != nil {
				return 0
			}
			params["cursor"] = chunk.Next
			poll, cancel := context.WithTimeout(ctx, 5*time.Second)
			result, rpcErr = api.Call(poll, method, params)
			cancel()
			if rpcErr != nil {
				if ctx.Err() != nil {
					return 0
				}
				return emitError(rpcErr, 1)
			}
		}
	}
	var value map[string]any
	if err := json.Unmarshal(result, &value); err == nil && value != nil {
		if artifact, ok := value["artifact"].(map[string]any); ok {
			id, _ := artifact["id"].(string)
			decodedID, err := hex.DecodeString(id)
			if err != nil || len(decodedID) != 16 {
				return emitError(fmt.Errorf("invalid remote artifact identifier"), 1)
			}
			sum, _ := artifact["sha256"].(string)
			path := filepath.Join(".local", "artifacts", id+".json")
			if *output != "" {
				path = *output
			}
			path, err = download(requestCtx, api, id, path, sum)
			if err != nil {
				return emitError(err, 1)
			}
			value["artifactPath"] = path
		}
		emit(value)
		if code, ok := value["exitCode"].(float64); ok && code != 0 {
			return 1
		}
		return 0
	}
	emit(json.RawMessage(result))
	return 0
}

func identitySecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if len(token) < 32 {
		return "", fmt.Errorf("guest secret must contain at least 32 characters")
	}
	return token, nil
}
func download(ctx context.Context, api caller, id, path, expected string) (string, error) {
	data := make([]byte, 0)
	offset := 0
	for {
		raw, err := api.Call(ctx, "artifact.read", map[string]any{"id": id, "offset": offset, "limit": 48 << 10})
		if err != nil {
			return "", err
		}
		var chunk struct {
			Data []byte `json:"data"`
			Next int    `json:"nextOffset"`
			EOF  bool   `json:"eof"`
		}
		if err := json.Unmarshal(raw, &chunk); err != nil {
			return "", err
		}
		if chunk.Next != offset+len(chunk.Data) || (!chunk.EOF && len(chunk.Data) == 0) {
			return "", fmt.Errorf("invalid artifact chunk")
		}
		data = append(data, chunk.Data...)
		if len(data) > 16<<20 {
			return "", fmt.Errorf("artifact exceeds 16 MiB")
		}
		offset = chunk.Next
		if chunk.EOF {
			break
		}
	}
	sum := sha256.Sum256(data)
	if expected != "" && hex.EncodeToString(sum[:]) != expected {
		return "", fmt.Errorf("artifact SHA-256 mismatch")
	}
	if err := writePrivate(path, data); err != nil {
		return "", err
	}
	return filepath.Abs(path)
}
