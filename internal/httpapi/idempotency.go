package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
)

// canonicalDigest returns a stable digest binding a request path and its body
// bytes. It is the canonical request summary stored with each idempotency key.
func canonicalDigest(path string, body []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(path))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}
