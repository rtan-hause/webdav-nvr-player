package signedurl

import (
	"strings"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	s := New()
	now := time.Unix(1_800_000_000, 0)
	tok := s.Sign("2026/11/06/Living Room_00_20261106215852.mp4", now.Add(time.Minute))
	if strings.ContainsAny(tok, "/+= ") {
		t.Fatalf("token must be URL-safe: %q", tok)
	}
	p, ok := s.Verify(tok, now)
	if !ok || p != "2026/11/06/Living Room_00_20261106215852.mp4" {
		t.Fatalf("Verify = %q %v", p, ok)
	}
	if _, ok := s.Verify(tok, now.Add(2*time.Minute)); ok {
		t.Fatal("expired token accepted")
	}
	if _, ok := New().Verify(tok, now); ok {
		t.Fatal("token from another key accepted")
	}
	payload, sig, _ := strings.Cut(tok, ".")
	if _, ok := s.Verify(payload[:len(payload)-2]+"AA."+sig, now); ok {
		t.Fatal("tampered path accepted")
	}
	for _, bad := range []string{"", ".", "abc", "abc.def"} {
		if _, ok := s.Verify(bad, now); ok {
			t.Fatalf("garbage %q accepted", bad)
		}
	}
}
