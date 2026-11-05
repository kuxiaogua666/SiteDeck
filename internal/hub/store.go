package hub

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"sitedeck/internal/access"
)

type Device struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	OS             string    `json:"os,omitempty"`
	Arch           string    `json:"arch,omitempty"`
	Version        string    `json:"version,omitempty"`
	Capabilities   []string  `json:"capabilities"`
	AccessProtocol int       `json:"accessProtocol"`
	FirstSeen      time.Time `json:"firstSeen"`
	LastSeen       time.Time `json:"lastSeen"`
	Blocked        bool      `json:"blocked"`
	AllowPaired    bool      `json:"allowPaired"`
}
type Grant struct {
	ID        string     `json:"id"`
	Device    string     `json:"deviceId"`
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt time.Time  `json:"expiresAt"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
}
type storedGrant struct {
	Grant
	Hash string `json:"tokenHash"`
}
type state struct {
	Version    int                    `json:"version"`
	SigningKey string                 `json:"signingKey"`
	Devices    map[string]Device      `json:"devices"`
	Grants     map[string]storedGrant `json:"grants"`
}

func newState() (state, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return state{}, err
	}
	return state{Version: 1, SigningKey: base64.RawStdEncoding.EncodeToString(key), Devices: map[string]Device{}, Grants: map[string]storedGrant{}}, nil
}
func loadState(path string) (state, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		s, err := newState()
		if err != nil {
			return s, err
		}
		return s, saveState(path, s)
	}
	if err != nil {
		return state{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil {
		return state{}, err
	}
	if len(b) > 8<<20 {
		return state{}, fmt.Errorf("Hub state exceeds 8 MiB")
	}
	var s state
	if err = json.Unmarshal(b, &s); err != nil {
		return state{}, fmt.Errorf("invalid Hub state: %w", err)
	}
	key, err := base64.RawStdEncoding.DecodeString(s.SigningKey)
	if err != nil || len(key) != ed25519.PrivateKeySize || s.Version != 1 || s.Devices == nil || s.Grants == nil {
		return state{}, fmt.Errorf("invalid Hub state identity or version")
	}
	if string(ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])) != string(key) {
		return state{}, fmt.Errorf("invalid Hub signing key")
	}
	for id, d := range s.Devices {
		if id != d.ID || id == "" {
			return state{}, fmt.Errorf("invalid persisted device identity")
		}
	}
	for id, g := range s.Grants {
		hash, err := hex.DecodeString(g.Hash)
		if err != nil || len(hash) != 32 || id != g.ID || !access.ValidScopes(g.Scopes) {
			return state{}, fmt.Errorf("invalid persisted access grant")
		}
		if _, exists := s.Devices[g.Device]; !exists {
			return state{}, fmt.Errorf("access grant references an unknown device")
		}
	}
	return s, nil
}
func (s state) clone() state {
	next := s
	next.Devices = make(map[string]Device, len(s.Devices))
	for id, d := range s.Devices {
		next.Devices[id] = d
	}
	next.Grants = make(map[string]storedGrant, len(s.Grants))
	for id, g := range s.Grants {
		next.Grants[id] = g
	}
	return next
}
func saveState(path string, s state) error {
	if path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > 8<<20 {
		return fmt.Errorf("Hub state exceeds 8 MiB")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".hub-state-*")
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
func (h *Hub) commitLocked(next state) error {
	if err := saveState(h.statePath, next); err != nil {
		return err
	}
	h.state = next
	return nil
}
func (h *Hub) signingKey() ed25519.PrivateKey {
	b, _ := base64.RawStdEncoding.DecodeString(h.state.SigningKey)
	return ed25519.PrivateKey(b)
}
