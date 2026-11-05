package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"sitedeck/internal/host"
	"sitedeck/internal/identity"
	"sitedeck/internal/protocol"
	"sitedeck/internal/serialport"
	"sitedeck/internal/session"
	"sitedeck/internal/sshclient"
)

type Operations struct {
	State         *identity.State
	Allowed       map[string]bool
	Sessions      *session.Manager
	serialMu      sync.Mutex
	artifactMu    sync.Mutex
	artifacts     map[string]artifact
	artifactBytes int
}
type artifact struct {
	data  []byte
	scope string
}

func NewOperations(state *identity.State, allow string) *Operations {
	allowed := map[string]bool{}
	for _, scope := range strings.Split(allow, ",") {
		scope = strings.TrimSpace(scope)
		if scope != "" {
			allowed[scope] = true
		}
	}
	return &Operations{State: state, Allowed: allowed, Sessions: session.NewManager(), artifacts: map[string]artifact{}}
}
func (o *Operations) effective(scopes map[string]bool) map[string]bool {
	allowed := map[string]bool{}
	for _, scope := range []string{"host", "ssh", "serial"} {
		allowed[scope] = o.Allowed[scope] && (scopes == nil || scopes[scope])
	}
	return allowed
}
func (o *Operations) LocalPermissions() map[string]bool { return o.effective(nil) }
func (o *Operations) permission(scope string, allowed map[string]bool) *protocol.Error {
	if !allowed[scope] {
		return protocol.Fail("PERMISSION_DENIED", "现场能力开关或当前访问授权未允许 "+scope+" 能力")
	}
	return nil
}
func (o *Operations) methods(allowed map[string]bool) []string {
	methods := []string{"doctor", "permissions.get", "session.list", "artifact.read", "request.cancel"}
	if allowed["host"] {
		methods = append(methods, "host.exec", "host.shell")
	}
	if allowed["ssh"] {
		methods = append(methods, "ssh.hostkey.scan", "ssh.exec", "ssh.shell")
	}
	if allowed["serial"] {
		methods = append(methods, "serial.list", "serial.connect", "serial.read", "serial.write", "serial.wait", "serial.exec", "serial.disconnect")
	}
	return append(methods, "session.attach", "session.detach", "session.read", "session.write", "session.resize", "session.close")
}
func (o *Operations) Handle(ctx context.Context, member string, connection map[string]any, req protocol.Request, allowed map[string]bool) (any, *protocol.Error) {
	if ctx.Err() != nil {
		return nil, protocol.Fail("CANCELED", ctx.Err().Error())
	}
	group := strings.Split(req.Method, ".")[0]
	if group == "host" || group == "ssh" || group == "serial" {
		if err := o.permission(group, allowed); err != nil {
			return nil, err
		}
	}
	switch req.Method {
	case "doctor":
		hostname, _ := os.Hostname()
		mode := "guest"
		if connection["grantId"] != nil {
			mode = "guest+hub-grant"
		}
		return map[string]any{"deviceId": o.State.Device, "version": protocol.Version, "protocolVersion": 1, "authenticated": true, "authMode": mode, "guestExpiresAt": o.State.GuestExpiresAt, "hostname": hostname, "os": runtime.GOOS, "arch": runtime.GOARCH, "connection": connection, "methods": o.methods(allowed)}, nil
	case "permissions.get":
		return allowed, nil
	case "host.exec":
		var p host.Params
		if err := protocol.Decode(req.Params, &p); err != nil {
			return nil, err
		}
		r, err := host.Exec(ctx, p)
		if err != nil {
			return nil, err
		}
		return o.output(r, "host"), nil
	case "host.shell":
		var p host.Params
		if err := protocol.Decode(req.Params, &p); err != nil {
			return nil, err
		}
		s, err := host.Shell(p)
		return o.add(s, err)
	case "ssh.hostkey.scan", "ssh.exec", "ssh.shell":
		var p sshclient.Params
		if err := protocol.Decode(req.Params, &p); err != nil {
			return nil, err
		}
		if req.Method == "ssh.hostkey.scan" {
			return sshclient.Scan(ctx, p)
		}
		if req.Method == "ssh.exec" {
			r, err := sshclient.Exec(ctx, p)
			if err != nil {
				return nil, err
			}
			return o.output(r, "ssh"), nil
		}
		s, err := sshclient.Shell(ctx, p)
		return o.add(s, err)
	case "serial.list":
		return serialport.List()
	case "serial.connect":
		var p serialport.Params
		if err := protocol.Decode(req.Params, &p); err != nil {
			return nil, err
		}
		name, err := serialport.Canonical(p.Port)
		if err != nil {
			return nil, protocol.Fail("DEVICE_NOT_FOUND", err.Error())
		}
		p.Port = name
		o.serialMu.Lock()
		defer o.serialMu.Unlock()
		for _, s := range o.Sessions.List() {
			if s["kind"] == "serial" && s["name"] == p.Port {
				return nil, protocol.Fail("RESOURCE_BUSY", "serial port already has an owner")
			}
		}
		s, rpcErr := serialport.Open(p)
		if rpcErr != nil {
			return nil, rpcErr
		}
		return o.add(s, nil)
	case "session.list":
		list := o.Sessions.List()
		visible := make([]map[string]any, 0, len(list))
		for _, s := range list {
			if allowed[s["kind"].(string)] {
				visible = append(visible, s)
			}
		}
		return visible, nil
	case "session.attach", "session.detach", "session.read", "session.write", "session.resize", "session.close", "serial.read", "serial.write", "serial.wait", "serial.exec", "serial.disconnect":
		return o.session(ctx, member, req, allowed)
	case "artifact.read":
		var p struct {
			ID     string `json:"id"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
		}
		if err := protocol.Decode(req.Params, &p); err != nil {
			return nil, err
		}
		o.artifactMu.Lock()
		defer o.artifactMu.Unlock()
		stored, ok := o.artifacts[p.ID]
		if !ok {
			return nil, protocol.Fail("ARTIFACT_NOT_FOUND", "artifact expired or not found")
		}
		if err := o.permission(stored.scope, allowed); err != nil {
			return nil, err
		}
		b := stored.data
		if p.Offset < 0 || p.Offset > len(b) {
			return nil, protocol.Fail("INVALID_PARAMS", "invalid offset")
		}
		if p.Limit <= 0 || p.Limit > 48<<10 {
			p.Limit = 48 << 10
		}
		end := p.Offset + p.Limit
		if end > len(b) {
			end = len(b)
		}
		return map[string]any{"data": b[p.Offset:end], "nextOffset": end, "eof": end == len(b)}, nil
	default:
		return nil, protocol.Fail("METHOD_NOT_FOUND", "unknown method")
	}
}
func (o *Operations) add(s *session.Session, err error) (any, *protocol.Error) {
	if err != nil {
		return nil, protocol.Fail("SESSION_OPEN_FAILED", err.Error())
	}
	if err = o.Sessions.Add(s); err != nil {
		s.Close()
		return nil, protocol.Fail("RESOURCE_BUSY", err.Error())
	}
	return s.Status(), nil
}
func (o *Operations) output(r *host.Output, scope string) any {
	// Larger responses retain complete captured bytes as a bounded artifact.
	b, _ := json.Marshal(r)
	if len(b) <= 16<<10 {
		return r
	}
	id := identity.Random(16)
	sum := sha256.Sum256(b)
	o.artifactMu.Lock()
	if o.artifactBytes+len(b) > 16<<20 {
		o.artifacts = map[string]artifact{}
		o.artifactBytes = 0
	}
	o.artifacts[id] = artifact{data: b, scope: scope}
	o.artifactBytes += len(b)
	o.artifactMu.Unlock()
	stdout, stderr := r.Stdout, r.Stderr
	if len(stdout) > 4096 {
		stdout = strings.ToValidUTF8(stdout[:4096], "�")
	}
	if len(stderr) > 4096 {
		stderr = strings.ToValidUTF8(stderr[:4096], "�")
	}
	return map[string]any{"stdout": stdout, "stderr": stderr, "exitCode": r.ExitCode, "timedOut": r.TimedOut, "truncated": true, "captureTruncated": r.Truncated, "artifact": map[string]any{"id": id, "size": len(b), "sha256": hex.EncodeToString(sum[:]), "format": "host-output-json"}}
}

type sessionParams struct {
	Source   string `json:"source"`
	ID       string `json:"id"`
	Cursor   string `json:"cursor"`
	Limit    int    `json:"limit"`
	WaitMS   int    `json:"waitMs"`
	Data     []byte `json:"data"`
	Text     string `json:"text"`
	ReadOnly bool   `json:"readOnly"`
	Rows     uint16 `json:"rows"`
	Cols     uint16 `json:"cols"`
	Pattern  string `json:"pattern"`
	Regex    bool   `json:"regex"`
}

func (o *Operations) session(ctx context.Context, member string, req protocol.Request, allowed map[string]bool) (any, *protocol.Error) {
	var p sessionParams
	if err := protocol.Decode(req.Params, &p); err != nil {
		return nil, err
	}
	s, err := o.Sessions.Get(p.ID)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(req.Method, "serial.") && s.Kind != "serial" {
		return nil, protocol.Fail("INVALID_PARAMS", "a serial session is required")
	}
	if err = o.permission(s.Kind, allowed); err != nil {
		return nil, err
	}
	switch req.Method {
	case "session.attach":
		s.Attach(member, p.ReadOnly)
		return s.Status(), nil
	case "session.detach":
		s.Detach(member, true)
		return map[string]any{"detached": true}, nil
	case "session.close", "serial.disconnect":
		return map[string]any{"closed": true}, o.Sessions.Close(p.ID)
	case "session.resize":
		return map[string]any{"resized": true}, s.Resize(member, p.Rows, p.Cols)
	case "session.read", "serial.read":
		if p.WaitMS < 0 || p.WaitMS > 30000 {
			return nil, protocol.Fail("INVALID_PARAMS", "waitMs must be 0..30000")
		}
		if p.Source == "" {
			p.Source = "all"
		}
		return s.ReadSource(ctx, p.Cursor, p.Limit, time.Duration(p.WaitMS)*time.Millisecond, p.Source)
	case "session.write", "serial.write":
		if s.Kind == "serial" {
			release, err := s.BeginSerial()
			if err != nil {
				return nil, err
			}
			defer release()
		}
		if len(p.Data) > 0 && p.Text != "" {
			return nil, protocol.Fail("INVALID_PARAMS", "provide data (base64) or text")
		}
		data := p.Data
		if p.Text != "" {
			data = []byte(p.Text)
		}
		return map[string]any{"written": len(data)}, s.Input(member, data)
	case "serial.wait", "serial.exec":
		if p.Pattern == "" || len(p.Pattern) > 4096 {
			return nil, protocol.Fail("INVALID_PARAMS", "a pattern up to 4096 bytes is required")
		}
		var re *regexp.Regexp
		if p.Regex {
			var e error
			re, e = regexp.Compile(p.Pattern)
			if e != nil {
				return nil, protocol.Fail("INVALID_PARAMS", e.Error())
			}
		}
		cursor := p.Cursor
		if cursor == "" {
			cursor = s.Cursor()
		}
		if req.Method == "serial.exec" {
			release, err := s.BeginSerial()
			if err != nil {
				return nil, err
			}
			defer release()
			cursor = s.Cursor()
			if err = s.Input(member, []byte(p.Text)); err != nil {
				return nil, err
			}
		}
		buf := make([]byte, 0)
		overflow := false
		for {
			r, e := s.ReadSource(ctx, cursor, 48<<10, time.Second, "rx")
			if e != nil {
				return nil, e
			}
			cursor = r.Next
			overflow = overflow || r.Overflow
			buf = append(buf, r.Data...)
			if len(buf) > session.BufferSize {
				buf = buf[len(buf)-session.BufferSize:]
				overflow = true
			}
			matched := false
			if re != nil {
				matched = re.Match(buf)
			} else {
				matched = strings.Contains(string(buf), p.Pattern)
			}
			if matched || r.EOF {
				return map[string]any{"matched": matched, "data": buf, "nextCursor": cursor, "overflow": overflow, "eof": r.EOF}, nil
			}
			select {
			case <-ctx.Done():
				return nil, protocol.Fail("TIMEOUT", "prompt was not observed before the deadline")
			default:
			}
		}
	}
	return nil, protocol.Fail("METHOD_NOT_FOUND", "unknown session method")
}
