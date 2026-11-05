package sshclient

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
	"sitedeck/internal/host"
	"sitedeck/internal/protocol"
	"sitedeck/internal/session"
)

type Params struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	Fingerprint string `json:"fingerprint"`
	Password    string `json:"password"`
	PrivateKey  string `json:"privateKey"`
	Passphrase  string `json:"passphrase"`
	Command     string `json:"command"`
	Rows        uint16 `json:"rows"`
	Cols        uint16 `json:"cols"`
}

func address(p Params) (string, error) {
	if p.Host == "" {
		return "", fmt.Errorf("host is required")
	}
	if p.Port == 0 {
		p.Port = 22
	}
	if p.Port < 1 || p.Port > 65535 {
		return "", fmt.Errorf("invalid SSH port")
	}
	return net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), nil
}
func Scan(ctx context.Context, p Params) (map[string]any, *protocol.Error) {
	addr, err := address(p)
	if err != nil {
		return nil, protocol.Fail("INVALID_PARAMS", err.Error())
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, protocol.Fail("SSH_CONNECT_FAILED", err.Error())
	}
	defer conn.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	var fingerprint, algorithm string
	_, _, _, _ = ssh.NewClientConn(conn, addr, &ssh.ClientConfig{User: "hostkey-scan", HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fingerprint = ssh.FingerprintSHA256(key)
		algorithm = key.Type()
		return fmt.Errorf("scan complete")
	}})
	if fingerprint == "" {
		return nil, protocol.Fail("SSH_HANDSHAKE_FAILED", "no host key received")
	}
	return map[string]any{"host": p.Host, "fingerprint": fingerprint, "algorithm": algorithm, "verified": false}, nil
}
func dial(ctx context.Context, p Params) (*ssh.Client, error) {
	addr, err := address(p)
	if err != nil {
		return nil, err
	}
	if p.User == "" || !strings.HasPrefix(p.Fingerprint, "SHA256:") {
		return nil, fmt.Errorf("user and a trusted SHA256 host fingerprint are required")
	}
	var auth []ssh.AuthMethod
	if p.Password != "" {
		auth = append(auth, ssh.Password(p.Password))
	}
	if p.PrivateKey != "" {
		var signer ssh.Signer
		if p.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(p.PrivateKey), []byte(p.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(p.PrivateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("invalid SSH private key")
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if len(auth) == 0 {
		return nil, fmt.Errorf("SSH password or privateKey is required")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	c, ch, req, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{User: p.User, Auth: auth, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if ssh.FingerprintSHA256(key) != p.Fingerprint {
			return fmt.Errorf("SSH_HOST_KEY_MISMATCH")
		}
		return nil
	}})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !stopCancel() || ctx.Err() != nil {
		conn.Close()
		return nil, ctx.Err()
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, ch, req), nil
}

type output struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (o *output) Write(b []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(b)
	left := protocol.MaxOutput - len(o.data)
	if n > left {
		b = b[:left]
		o.truncated = true
	}
	o.data = append(o.data, b...)
	return n, nil
}
func Exec(ctx context.Context, p Params) (*host.Output, *protocol.Error) {
	if p.Command == "" {
		return nil, protocol.Fail("INVALID_PARAMS", "SSH command is required")
	}
	c, err := dial(ctx, p)
	if err != nil {
		return nil, protocol.Fail("SSH_CONNECT_FAILED", err.Error())
	}
	defer c.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stopCancel()
	s, err := c.NewSession()
	if err != nil {
		return nil, protocol.Fail("SSH_SESSION_FAILED", err.Error())
	}
	defer s.Close()
	stdout, stderr := &output{}, &output{}
	s.Stdout = stdout
	s.Stderr = stderr
	err = s.Run(p.Command)
	code := 0
	if err != nil {
		if exit, ok := err.(*ssh.ExitError); ok {
			code = exit.ExitStatus()
		} else if ctx.Err() != nil {
			code = -1
		} else {
			return nil, protocol.Fail("SSH_EXEC_FAILED", err.Error())
		}
	}
	r := &host.Output{Stdout: strings.ToValidUTF8(string(stdout.data), "�"), Stderr: strings.ToValidUTF8(string(stderr.data), "�"), ExitCode: code, Truncated: stdout.truncated || stderr.truncated, TimedOut: ctx.Err() != nil}
	if !utf8.Valid(stdout.data) {
		r.RawStdout = stdout.data
	}
	if !utf8.Valid(stderr.data) {
		r.RawStderr = stderr.data
	}
	return r, nil
}

type shellCloser struct {
	client  *ssh.Client
	session *ssh.Session
}

func (c shellCloser) Close() error { _ = c.session.Close(); return c.client.Close() }
func Shell(ctx context.Context, p Params) (*session.Session, error) {
	c, err := dial(ctx, p)
	if err != nil {
		return nil, err
	}
	stopCancel := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stopCancel()
	ss, err := c.NewSession()
	if err != nil {
		c.Close()
		return nil, err
	}
	fail := func(err error) (*session.Session, error) { ss.Close(); c.Close(); return nil, err }
	if p.Rows == 0 {
		p.Rows = 24
	}
	if p.Cols == 0 {
		p.Cols = 80
	}
	if err = ss.RequestPty("xterm-256color", int(p.Rows), int(p.Cols), ssh.TerminalModes{}); err != nil {
		return fail(err)
	}
	stdin, err := ss.StdinPipe()
	if err != nil {
		return fail(err)
	}
	s := session.New("ssh", p.User+"@"+p.Host, stdin, shellCloser{client: c, session: ss}, func(rows, cols uint16) error { return ss.WindowChange(int(rows), int(cols)) })
	ss.Stdout = s
	ss.Stderr = s
	if err = ss.Shell(); err != nil {
		return fail(err)
	}
	if !stopCancel() || ctx.Err() != nil {
		return fail(ctx.Err())
	}
	go func() {
		err := ss.Wait()
		code := 0
		if err != nil {
			code = -1
			if e, ok := err.(*ssh.ExitError); ok {
				code = e.ExitStatus()
			}
		}
		s.End(code)
		c.Close()
	}()
	return s, nil
}

var _ io.Writer = (*output)(nil)
