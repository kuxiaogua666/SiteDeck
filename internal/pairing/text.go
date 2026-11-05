// Package pairing encodes a complete device pairing profile as a single line.
// The text is a bearer credential, not a public device identifier.
package pairing

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"sitedeck/internal/transport"
)

const Prefix = "SITEPAIR1."
const MaxText = 8192

type Profile struct {
	Hub         string    `json:"hub"`
	Device      string    `json:"deviceId"`
	Fingerprint string    `json:"fingerprint"`
	Token       string    `json:"token"`
	Transport   string    `json:"transport"`
	Expires     time.Time `json:"guestExpiresAt,omitempty"`
}
type compact struct {
	Version     int    `json:"v"`
	Hub         string `json:"h"`
	Device      string `json:"d"`
	Fingerprint string `json:"f"`
	Token       string `json:"k"`
	Expires     int64  `json:"e"`
}

func Validate(p Profile) error {
	if err := transport.ValidateEndpoint(p.Hub); err != nil {
		return fmt.Errorf("配对文本的 Hub 地址无效")
	}
	if p.Device == "" || len(p.Device) > 128 || !strings.HasPrefix(p.Device, "dev-") {
		return fmt.Errorf("配对文本的设备 ID 无效")
	}
	for _, r := range p.Device {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return fmt.Errorf("配对文本的设备 ID 无效")
		}
	}
	pin := strings.ToLower(strings.ReplaceAll(p.Fingerprint, ":", ""))
	b, err := hex.DecodeString(pin)
	if err != nil || len(b) != 32 {
		return fmt.Errorf("配对文本的证书指纹无效")
	}
	if len(p.Token) < 32 || len(p.Token) > 4096 {
		return fmt.Errorf("配对文本的访问秘钥无效")
	}
	for _, r := range p.Token {
		if r < 33 || r > 126 {
			return fmt.Errorf("配对文本的访问秘钥无效")
		}
	}
	if p.Expires.IsZero() || !time.Now().Before(p.Expires) {
		return fmt.Errorf("配对文本已到期，请在现场设备轮换秘钥并重新获取")
	}
	return nil
}
func Encode(p Profile) (string, error) {
	if err := Validate(p); err != nil {
		return "", err
	}
	wire := compact{Version: 1, Hub: p.Hub, Device: p.Device, Fingerprint: strings.ToLower(strings.ReplaceAll(p.Fingerprint, ":", "")), Token: p.Token, Expires: p.Expires.Unix()}
	b, err := json.Marshal(wire)
	if err != nil {
		return "", err
	}
	text := Prefix + base64.RawURLEncoding.EncodeToString(b)
	if len(text) > MaxText {
		return "", fmt.Errorf("配对文本超过大小限制")
	}
	return text, nil
}
func Decode(text string) (Profile, error) {
	text = strings.TrimSpace(text)
	if len(text) <= MaxText {
		text = strings.Join(strings.Fields(text), "")
	}
	if len(text) > MaxText || !strings.HasPrefix(text, Prefix) {
		return Profile{}, fmt.Errorf("配对文本格式无效，需要完整的 SITEPAIR1. 开头文本")
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(text, Prefix))
	if err != nil {
		return Profile{}, fmt.Errorf("配对文本编码无效")
	}
	var wire compact
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF || wire.Version != 1 {
		return Profile{}, fmt.Errorf("配对文本内容或版本无效")
	}
	p := Profile{Hub: wire.Hub, Device: wire.Device, Fingerprint: wire.Fingerprint, Token: wire.Token, Transport: "auto", Expires: time.Unix(wire.Expires, 0).UTC()}
	return p, Validate(p)
}
