// Package signedurl makes short-lived tokens that name a file path, so a local
// helper (ffmpeg) can fetch a recording through the app without credentials in
// its command line.
package signedurl

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"time"
)

// Signer signs and verifies tokens with a random per-process key.
type Signer struct{ key []byte }

// New returns a Signer with a fresh random key.
func New() *Signer {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return &Signer{key: k}
}

var enc = base64.RawURLEncoding

// Sign returns a URL-safe token for path that expires at exp.
func (s *Signer) Sign(path string, exp time.Time) string {
	payload := make([]byte, 8+len(path))
	binary.BigEndian.PutUint64(payload, uint64(exp.Unix()))
	copy(payload[8:], path)
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(s.mac(payload))
}

// Verify returns the path in token if the signature is valid and it hasn't expired.
func (s *Signer) Verify(token string, now time.Time) (string, bool) {
	p, m, ok := strings.Cut(token, ".")
	if !ok {
		return "", false
	}
	payload, err := enc.DecodeString(p)
	if err != nil || len(payload) < 8 {
		return "", false
	}
	sig, err := enc.DecodeString(m)
	if err != nil || !hmac.Equal(sig, s.mac(payload)) {
		return "", false
	}
	if now.Unix() > int64(binary.BigEndian.Uint64(payload)) {
		return "", false
	}
	return string(payload[8:]), true
}

func (s *Signer) mac(payload []byte) []byte {
	h := hmac.New(sha256.New, s.key)
	h.Write(payload)
	return h.Sum(nil)[:16]
}
