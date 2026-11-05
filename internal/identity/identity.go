package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type State struct {
	Device         string    `json:"deviceId"`
	Name           string    `json:"name,omitempty"`
	Certificate    string    `json:"certificate"`
	PrivateKey     string    `json:"privateKey"`
	GuestToken     string    `json:"guestToken"`
	GuestExpiresAt time.Time `json:"guestExpiresAt"`
}

func Random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func Load(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s State
	if err = json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.Device == "" || len(s.GuestToken) < 32 {
		return nil, fmt.Errorf("invalid identity state")
	}
	_, err = s.TLSCertificate()
	return &s, err
}

func Init(path string, lifetime ...time.Duration) (*State, error) {
	if s, err := Load(path); err == nil {
		return s, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	device := "dev-" + Random(12)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: device}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	pk, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	ttl := 24 * time.Hour
	if len(lifetime) > 0 {
		ttl = lifetime[0]
	}
	s := &State{Device: device, Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk})), GuestToken: Random(32), GuestExpiresAt: time.Now().Add(ttl)}
	if err := s.Save(path); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *State) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".identity-*")
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

func (s *State) TLSCertificate() (tls.Certificate, error) {
	return tls.X509KeyPair([]byte(s.Certificate), []byte(s.PrivateKey))
}
func Fingerprint(der []byte) string { h := sha256.Sum256(der); return hex.EncodeToString(h[:]) }
func (s *State) Fingerprint() string {
	c, _ := s.TLSCertificate()
	return Fingerprint(c.Certificate[0])
}

func ReadSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if len(token) < 32 {
		return "", fmt.Errorf("secret must contain at least 32 characters")
	}
	return token, nil
}
