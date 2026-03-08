package proxy

import (
	"crypto/rand"
	"encoding/hex"
)

// randomHex returns a random hex string of n bytes (2n characters).
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
