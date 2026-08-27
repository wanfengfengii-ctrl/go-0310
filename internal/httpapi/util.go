package httpapi

import (
	"crypto/rand"
	"encoding/hex"
)

// newRunID returns a random run identifier for client-supplied omissions.
func newRunID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
