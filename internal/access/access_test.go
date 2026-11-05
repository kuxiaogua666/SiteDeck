package access

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestTicketBindsIdentityChannelAndDeadline(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	claims := Claims{Version: 1, Device: "device-a", Channel: "channel-a", Grant: "grant-a", Scopes: []string{"serial"}, ExpiresAtMS: now.Add(time.Hour).UnixMilli(), HandshakeUntilMS: now.Add(time.Minute).UnixMilli()}
	token, err := Sign(private, claims)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.RawStdEncoding.EncodeToString(public)
	if got, err := Verify(key, token, claims.Device, claims.Channel, now); err != nil || got.Grant != claims.Grant {
		t.Fatal("valid grant rejected")
	}
	for _, test := range []struct {
		name, device, channel string
		at                    time.Time
	}{{"another device", "device-b", "channel-a", now}, {"another channel", "device-a", "channel-b", now}, {"handshake expired", "device-a", "channel-a", now.Add(2 * time.Minute)}, {"access expired", "device-a", "channel-a", now.Add(2 * time.Hour)}} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Verify(key, token, test.device, test.channel, test.at); err == nil {
				t.Fatal("access ticket accepted outside its binding")
			}
		})
	}
	parts := strings.Split(token, ".")
	body, _ := base64.RawURLEncoding.DecodeString(parts[0])
	tampered := strings.Replace(string(body), "serial", "host", 1)
	forged := base64.RawURLEncoding.EncodeToString([]byte(tampered)) + "." + parts[1]
	if _, err := Verify(key, forged, claims.Device, claims.Channel, now); err == nil {
		t.Fatal("modified permissions accepted without a new signature")
	}
}
