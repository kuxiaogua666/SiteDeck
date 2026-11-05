package pairing

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func sample() Profile {
	return Profile{Hub: "wss://example.test:48763/ws", Device: "dev-123abc", Fingerprint: strings.Repeat("a", 64), Token: strings.Repeat("b", 64), Expires: time.Now().Add(time.Hour)}
}
func TestPairingTextRoundTrip(t *testing.T) {
	p := sample()
	text, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(text)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Device != p.Device || decoded.Token != p.Token || decoded.Fingerprint != p.Fingerprint || decoded.Hub != p.Hub || decoded.Expires.Unix() != p.Expires.Unix() {
		t.Fatal("pairing profile changed")
	}
	wrapped := text[:25] + "\r\n" + text[25:]
	if _, err = Decode(wrapped); err != nil {
		t.Fatal("console wrapping must be accepted", err)
	}
}
func TestPairingTextRejectsInvalidCredentials(t *testing.T) {
	p := sample()
	p.Expires = time.Now().Add(-time.Minute)
	if _, err := Encode(p); err == nil {
		t.Fatal("expired pairing exported")
	}
	p = sample()
	p.Hub = "ws://192.168.1.2/ws"
	if _, err := Encode(p); err == nil {
		t.Fatal("insecure remote URL accepted")
	}
	p = sample()
	p.Fingerprint = "bad"
	if _, err := Encode(p); err == nil {
		t.Fatal("invalid pin accepted")
	}
	for _, text := range []string{"invalid", Prefix + "!", strings.Repeat("a", MaxText+1), Prefix + base64.RawURLEncoding.EncodeToString([]byte(`{"v":2}`)), Prefix + base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"surprise":1}`))} {
		if _, err := Decode(text); err == nil {
			t.Fatal("invalid text accepted")
		}
	}
	p = sample()
	p.Token = "SECRET_WITH_SPACE_" + strings.Repeat(" ", 40)
	_, err := Encode(p)
	if err == nil || strings.Contains(err.Error(), p.Token) {
		t.Fatal("invalid secret accepted or echoed")
	}
}
