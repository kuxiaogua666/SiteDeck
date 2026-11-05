// Package access defines Hub-signed, channel-bound access grants.
package access

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Claims struct {
	Version          int      `json:"version"`
	Device           string   `json:"deviceId"`
	Channel          string   `json:"channelId"`
	Grant            string   `json:"grantId"`
	Scopes           []string `json:"scopes"`
	ExpiresAtMS      int64    `json:"expiresAtMs"`
	HandshakeUntilMS int64    `json:"handshakeUntilMs"`
}

func ValidScopes(scopes []string) bool {
	if len(scopes) == 0 || len(scopes) > 3 {
		return false
	}
	seen := map[string]bool{}
	for _, scope := range scopes {
		if (scope != "host" && scope != "ssh" && scope != "serial") || seen[scope] {
			return false
		}
		seen[scope] = true
	}
	return true
}
func Sign(key ed25519.PrivateKey, claims Claims) (string, error) {
	if len(key) != ed25519.PrivateKeySize || !ValidScopes(claims.Scopes) {
		return "", fmt.Errorf("invalid access signing input")
	}
	b, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, b)), nil
}
func Verify(publicKey, token, device, channel string, now time.Time) (*Claims, error) {
	if len(token) > 4096 {
		return nil, fmt.Errorf("access ticket is too large")
	}
	key, err := base64.RawStdEncoding.DecodeString(publicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid Hub signing key")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid Hub access ticket")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid access claims")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !ed25519.Verify(key, body, signature) {
		return nil, fmt.Errorf("invalid Hub access signature")
	}
	var claims Claims
	if err = json.Unmarshal(body, &claims); err != nil {
		return nil, err
	}
	if claims.Version != 1 || claims.Device != device || claims.Channel != channel || claims.Grant == "" || !ValidScopes(claims.Scopes) {
		return nil, fmt.Errorf("access ticket does not belong to this channel")
	}
	if now.UnixMilli() >= claims.ExpiresAtMS || now.UnixMilli() >= claims.HandshakeUntilMS {
		return nil, fmt.Errorf("Hub access ticket expired")
	}
	return &claims, nil
}
