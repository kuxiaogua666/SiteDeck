package transport

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"sitedeck/internal/identity"
)

type authMessage struct {
	Nonce string `json:"nonce,omitempty"`
	Proof string `json:"proof,omitempty"`
	OK    bool   `json:"ok,omitempty"`
}

// These v1 domain labels are frozen wire-format identifiers. They intentionally
// retain the original values so SiteDeck can authenticate existing deployed
// Agents and existing pairing profiles without rotating device identities.
func proof(token, device, channel, nonce, role string, binding []byte) string {
	h := hmac.New(sha256.New, []byte(token))
	for _, part := range []string{"open-endlesst/1", device, channel, nonce, role, hex.EncodeToString(binding)} {
		_, _ = io.WriteString(h, part)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
func tlsClient(ctx context.Context, conn net.Conn, pin string) (*tls.Conn, error) {
	pin = strings.ToLower(strings.ReplaceAll(pin, ":", ""))
	if b, err := hex.DecodeString(pin); err != nil || len(b) != 32 {
		return nil, fmt.Errorf("Agent SHA-256 fingerprint is required")
	}
	c := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, // Trust is the explicitly paired certificate fingerprint.
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) != 1 {
				return fmt.Errorf("unexpected Agent certificate chain")
			}
			cert := cs.PeerCertificates[0]
			if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
				return fmt.Errorf("Agent certificate expired or not yet valid")
			}
			if subtle.ConstantTimeCompare([]byte(identity.Fingerprint(cert.Raw)), []byte(pin)) != 1 {
				return fmt.Errorf("AGENT_IDENTITY_MISMATCH")
			}
			return nil
		},
	})
	if err := c.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	return c, nil
}
func SecureClient(ctx context.Context, conn net.Conn, pin, token, device, channel string) (*tls.Conn, error) {
	c, err := tlsClient(ctx, conn, pin)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.SetDeadline(deadline)
	dec := json.NewDecoder(io.LimitReader(c, 4096))
	var challenge authMessage
	if err = dec.Decode(&challenge); err != nil {
		return nil, err
	}
	if len(challenge.Nonce) != 64 {
		return nil, fmt.Errorf("invalid authentication challenge")
	}
	state := c.ConnectionState()
	binding, err := state.ExportKeyingMaterial("open-endlesst-auth", nil, 32)
	if err != nil {
		return nil, err
	}
	enc := json.NewEncoder(c)
	if err = enc.Encode(authMessage{Proof: proof(token, device, channel, challenge.Nonce, "client", binding)}); err != nil {
		return nil, err
	}
	var response authMessage
	if err = dec.Decode(&response); err != nil {
		return nil, err
	}
	expected := proof(token, device, channel, challenge.Nonce, "agent", binding)
	if !response.OK || !hmac.Equal([]byte(expected), []byte(response.Proof)) {
		return nil, fmt.Errorf("AUTH_FAILED")
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

func SecureServer(ctx context.Context, conn net.Conn, state *identity.State, channel string) (*tls.Conn, error) {
	if !state.GuestExpiresAt.IsZero() && !time.Now().Before(state.GuestExpiresAt) {
		return nil, fmt.Errorf("GUEST_ACCESS_EXPIRED")
	}
	cert, err := state.TLSCertificate()
	if err != nil {
		return nil, err
	}
	c := tls.Server(conn, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}})
	if err = c.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	nonce := identity.Random(32)
	enc := json.NewEncoder(c)
	if err = enc.Encode(authMessage{Nonce: nonce}); err != nil {
		return nil, err
	}
	// A one-line reader avoids buffering the first RPC past the authentication boundary.
	reader := bufio.NewReader(io.LimitReader(c, 4096))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var response authMessage
	if err = json.Unmarshal(line, &response); err != nil {
		return nil, err
	}
	tlsState := c.ConnectionState()
	binding, err := tlsState.ExportKeyingMaterial("open-endlesst-auth", nil, 32)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal([]byte(response.Proof), []byte(proof(state.GuestToken, state.Device, channel, nonce, "client", binding))) {
		return nil, fmt.Errorf("AUTH_FAILED")
	}
	if err = enc.Encode(authMessage{OK: true, Proof: proof(state.GuestToken, state.Device, channel, nonce, "agent", binding)}); err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}
