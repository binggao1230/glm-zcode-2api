package convert

import (
	"crypto/rand"
	"encoding/hex"
)

func randomSuffix() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "000000000000000000000000"
	}
	return hex.EncodeToString(buf[:])
}
